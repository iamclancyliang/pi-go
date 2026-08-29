package deepseek_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// recordedRejection is the real refusal, read from the probe's fixture rather
// than written here.
//
// A hand-written string would test the matcher against the author's memory of
// the provider. This tests it against what the provider actually sent, which is
// the whole reason the probe was authorized.
func recordedRejection(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../../conformance/testdata/deepseek-large-request-rejected.json")
	if err != nil {
		t.Fatalf("reading the recorded rejection: %v", err)
	}
	var fixture struct {
		Outcome string `json:"outcome"`
		Status  int    `json:"status"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("the fixture is unreadable: %v", err)
	}
	if fixture.Outcome != "rejected" || fixture.Status != 400 {
		t.Fatalf("the fixture records %s at %d, not a 400 rejection", fixture.Outcome, fixture.Status)
	}
	return fixture.Body
}

// TestTheRecordedRejectionIsRecognisedAsAnOverflow. This is the behaviour the
// probe was spent on: without it the refusal ends the turn, and
// compact-before-retry never runs for the up-front path.
func TestTheRecordedRejectionIsRecognisedAsAnOverflow(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return status(400, recordedRejection(t))
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "sk-probe-test-credential"})

	_, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	if err == nil {
		t.Fatal("an oversized request was reported as a success")
	}
	if !errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("the refusal came back as %v, not an overflow the runtime can recover from", err)
	}
	// The numbers travel with it: a recovery that cannot say how far over it
	// was cannot tell whether shortening once will be enough.
	if !strings.Contains(err.Error(), "1048576") {
		t.Fatalf("the window is not named in %v", err)
	}
}

// TestAnOrdinaryBadRequestIsNotAnOverflow. The provider uses the same status,
// type and code for any malformed request, so a detector that stopped at those
// would turn every rejected field name into a recoverable overflow — and each
// one would cost a summarisation call before failing anyway.
func TestAnOrdinaryBadRequestIsNotAnOverflow(t *testing.T) {
	others := map[string]string{
		"an unknown field": `{"error":{"message":"Unrecognized request argument supplied: fooo","type":"invalid_request_error","code":"invalid_request_error"}}`,
		"a bad value":      `{"error":{"message":"Invalid value for 'temperature': must be <= 2","type":"invalid_request_error","code":"invalid_request_error"}}`,
		"no message":       `{"error":{"type":"invalid_request_error","code":"invalid_request_error"}}`,
		"not json":         `<html>502 Bad Gateway</html>`,
	}
	for name, body := range others {
		t.Run(name, func(t *testing.T) {
			tr := &countingTransport{respond: func(int) *http.Response { return status(400, body) }}
			p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "sk-probe-test-credential"})

			_, err := p.Generate(context.Background(), ai.Request{Model: "m"})
			if err == nil {
				t.Fatal("a rejected request was reported as a success")
			}
			if errors.Is(err, ai.ErrContextOverflow) {
				t.Fatalf("%s was mistaken for an overflow: %v", name, err)
			}
		})
	}
}

// TestAMessageMentioningAContextLengthIsNotEnough. The condition is that the
// request EXCEEDED the window, and a message naming both numbers without that
// is something else — a warning, a note, a different provider's wording.
func TestAMessageMentioningAContextLengthIsNotEnough(t *testing.T) {
	within := `{"error":{"message":"This model's maximum context length is 1048576 tokens. However, you requested 500 tokens (484 in the messages, 16 in the completion).","type":"invalid_request_error","code":"invalid_request_error"}}`
	tr := &countingTransport{respond: func(int) *http.Response { return status(400, within) }}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "sk-probe-test-credential"})

	_, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	if errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("a request within the window was called an overflow: %v", err)
	}
}

// TestOnlyA400IsExamined. The same sentence arriving with a 500 is a server
// fault echoing something, not a refusal about this request.
func TestOnlyA400IsExamined(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return status(500, recordedRejection(t))
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "sk-probe-test-credential"})

	_, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	if errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("a 500 was read as an up-front overflow: %v", err)
	}
}

// TestAnOverflowRefusalIsNotRetriedAsTransient. Sending the same oversized
// request again is a second billed rejection and cannot succeed.
func TestAnOverflowRefusalIsNotRetriedAsTransient(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return status(400, recordedRejection(t))
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "sk-probe-test-credential"})

	_, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if ai.Retryable(err) {
		t.Fatalf("an overflow was reported as worth repeating unchanged: %v", err)
	}
	if tr.requests != 1 {
		t.Fatalf("an overflow refusal was sent %d times", tr.requests)
	}
}
