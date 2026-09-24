package tools

import (
	"os"
	"strings"
	"testing"
)

func TestBashCapturePreservesSplitUTF8AndNormalizesCarriageReturns(t *testing.T) {
	var chunks []string
	capture := newBashCapture(func(chunk string) { chunks = append(chunks, chunk) }, Limits{})
	if _, err := capture.Write([]byte("prefix\r")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := capture.Write([]byte{0xc3}); err != nil {
		t.Fatalf("split UTF-8 lead byte: %v", err)
	}
	if _, err := capture.Write([]byte{0xa9}); err != nil {
		t.Fatalf("split UTF-8 continuation byte: %v", err)
	}
	if _, err := capture.Write([]byte("suffix\r\n")); err != nil {
		t.Fatalf("last write: %v", err)
	}
	got, _, err := capture.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if got != "prefixésuffix\n" || strings.Join(chunks, "") != got {
		t.Fatalf("capture=%q chunks=%q", got, strings.Join(chunks, ""))
	}
}

func TestBashCaptureRemovesOSC8HyperlinksAndOSCControls(t *testing.T) {
	capture := newBashCapture(nil, Limits{})
	input := "before\x1b]8;;https://example.com\x07label\x1b]8;;\x1b\\ after"
	input += "\x1b]0;window title\x1b\\"
	input += " c1\x9d8;;https://example.com\x9clater\x9d0;title\x9c"
	if _, err := capture.Write([]byte(input)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _, err := capture.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if want := "beforelabel after c1later"; got != want {
		t.Fatalf("sanitized output=%q, want %q", got, want)
	}
}

func TestBashCaptureHoldsOSC8UntilTheTerminatorArrives(t *testing.T) {
	capture := newBashCapture(nil, Limits{})
	for _, chunk := range []string{
		"left\x1b]8;;https://example.com",
		"\x07middle\x1b]8;;",
		"\x1b\\right",
	} {
		if _, err := capture.Write([]byte(chunk)); err != nil {
			t.Fatalf("write %q: %v", chunk, err)
		}
	}
	got, _, err := capture.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if want := "leftmiddleright"; got != want {
		t.Fatalf("sanitized split output=%q, want %q", got, want)
	}
}

func TestBashCapturePreservesUTF8AroundOSCAtEveryByteBoundary(t *testing.T) {
	input := "保留\x1b]8;;https://example.com\x07输出\x1b]8;;\x1b\\中文"
	for split := 0; split <= len(input); split++ {
		capture := newBashCapture(nil, Limits{})
		for i := 0; i < split; i++ {
			if _, err := capture.Write([]byte{input[i]}); err != nil {
				t.Fatalf("split %d, byte %d: %v", split, i, err)
			}
		}
		if _, err := capture.Write([]byte(input[split:])); err != nil {
			t.Fatalf("split %d, remainder: %v", split, err)
		}
		got, _, err := capture.Finish()
		if err != nil {
			t.Fatalf("split %d, finish: %v", split, err)
		}
		if want := "保留输出中文"; got != want {
			t.Fatalf("split %d: output=%q, want %q", split, got, want)
		}
	}
}

func TestSanitizeBashOutputPreservesUTF8C1ContinuationBytes(t *testing.T) {
	if got, want := sanitizeBashOutput([]byte("保留输出")), "保留输出"; got != want {
		t.Fatalf("output=%q, want %q", got, want)
	}
}

func TestBashCaptureRemovesUTF8EncodedC1OSCAtEveryByteBoundary(t *testing.T) {
	input := "before\x1b]0;title\u009cafter \u009d8;;https://example.com\u009cshown"
	for split := 0; split <= len(input); split++ {
		capture := newBashCapture(nil, Limits{})
		for i := 0; i < split; i++ {
			if _, err := capture.Write([]byte{input[i]}); err != nil {
				t.Fatalf("split %d, byte %d: %v", split, i, err)
			}
		}
		if _, err := capture.Write([]byte(input[split:])); err != nil {
			t.Fatalf("split %d, remainder: %v", split, err)
		}
		got, _, err := capture.Finish()
		if err != nil {
			t.Fatalf("split %d, finish: %v", split, err)
		}
		if want := "beforeafter shown"; got != want {
			t.Fatalf("split %d: output=%q, want %q", split, got, want)
		}
	}
}

func TestSanitizeBashOutputRemovesStandaloneUTF8C1Controls(t *testing.T) {
	if got, want := sanitizeBashOutput([]byte("before\u009cafter")), "beforeafter"; got != want {
		t.Fatalf("ST output=%q, want %q", got, want)
	}
	if got, want := sanitizeBashOutput([]byte("before\u009d")), "before"; got != want {
		t.Fatalf("OSC output=%q, want %q", got, want)
	}
}

func TestBashCaptureBoundsUnterminatedOSCPending(t *testing.T) {
	capture := newBashCapture(nil, Limits{})
	if _, err := capture.Write([]byte("prefix\x1b]8;;")); err != nil {
		t.Fatalf("prefix: %v", err)
	}
	for i := 0; i < 1024; i++ {
		if _, err := capture.Write([]byte(strings.Repeat("x", 1024))); err != nil {
			t.Fatalf("payload %d: %v", i, err)
		}
		if len(capture.pending) > 8 {
			t.Fatalf("unterminated OSC pending grew to %d bytes", len(capture.pending))
		}
	}
	got, _, err := capture.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if got != "prefix" {
		t.Fatalf("output=%q, want %q", got, "prefix")
	}
}

func TestBashCaptureKeepsRollingMemoryAndSpillsCompleteOutput(t *testing.T) {
	capture := newBashCapture(nil, Limits{MaxBytes: 8})
	const chunk = "0123456789"
	for i := 0; i < 100; i++ {
		if _, err := capture.Write([]byte(chunk)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	got, path, err := capture.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	defer os.Remove(path)
	if len(capture.rolling) > 16 {
		t.Fatalf("rolling capture grew to %d bytes", len(capture.rolling))
	}
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if string(full) != strings.Repeat(chunk, 100) {
		t.Fatalf("spill lost output: got %d bytes", len(full))
	}
	if got != string(full[len(full)-len(got):]) {
		t.Fatalf("rolling result is not the output tail: %q", got)
	}
}
