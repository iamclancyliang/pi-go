# ADR-0011: The interface renderer — Pi's line model, built here rather than adopted

**Status:** accepted — @qy-liang, 2026-10-24
**Date:** 2026-10-24
**Decision owner:** @qy-liang
**Related:** ADR-0001, ADR-0002 · `docs/product/pi-feature-inventory.md` §11.4–11.6 ·
`docs/specs/tui-split-plan.md` · issue #28 (deviation D-7)

## Context

pi-go's interactive mode is a line-based loop over a line editor (D-7). Pi's is a terminal
interface: `packages/tui` (about 16.7k lines) under a 6.4k-line interactive mode. The interface plan
splits the port into slices; this ADR settles the one decision every slice rests on — what draws
the screen.

The census (§11.4–11.6) recorded the facts that decide it:

1. **Pi's default mode never leaves the main screen.** The conversation is written into the
   terminal's own scrollback and only the region still on screen is rewritten. The alternate-screen
   mode exists and is marked experimental.
2. **Pi's model is small and framework-free.** A component renders itself to lines for a width; a
   frame diffs the new lines against the last ones and rewrites only the changed range, inside
   synchronized output, with a reset at the end of every line. A zero-width marker in a focused
   component's output places the hardware cursor, which is what positions an input method's
   candidate window.
3. **Its whole dependency surface is two libraries**: a Markdown parser and East Asian Width, used
   per grapheme cluster with RGI emoji counted as two cells.

## Options

| | What it is | Against it |
| --- | --- | --- |
| **A** | Build Pi's model in Go: `Render(width) []string`, the same diff, the same cursor marker | The terminal edge cases are ours to handle: synchronized output, bracketed paste, the Kitty keyboard protocol, wide and combining characters |
| B | bubbletea and lipgloss | It owns the event loop and the state model. Its inline mode redraws one view region; Pi's conversation lives in scrollback with only changed lines rewritten. Getting Pi's behaviour means working around the framework, and the behaviour a user sees would be set by whichever the framework's redraw happens to be |
| C | tcell or tview | Alternate screen by construction — the opposite of Pi's default mode |

## Decision

**Option A.** The interface is built on Pi's line model in `internal/tui`, owned by this
repository.

The reasoning is ADR-0002's: what a user sees is product contract, and a framework whose redraw
semantics differ from Pi's would decide that contract by accident. Pi's model needs nothing a
framework provides — its renderer is a diff over string slices — and keeping it as data makes every
frame testable as data.

Two dependencies follow, and no more:

* **`github.com/rivo/uniseg`** for grapheme clusters and their cell width — Pi's
  `Intl.Segmenter` plus East Asian Width plus RGI emoji. It is already in the module graph,
  indirectly; the rendering core takes it directly.
* **`github.com/yuin/goldmark`**, already a dependency for the HTML export, parses Markdown for the
  chat transcript. Its tree is rendered to terminal lines here, as Pi renders `marked`'s tokens.

`golang.org/x/term`, already used, provides raw mode and the terminal size. `golang.org/x/sys`,
already in the graph beneath it, is used directly for `select(2)`: a terminal device cannot join
Go's poller on macOS, so read deadlines do nothing there, and the reader waits for input with a
timeout instead in order to notice when it should stop. macOS's `poll(2)` reports a terminal as
invalid, which is why it is `select`.

## Consequences

* **Every frame is tested as a screen, not as bytes.** The renderer writes to an `io.Writer`; tests
  feed that output to a small terminal emulator kept with the tests, which applies the cursor moves,
  line clears, screen and scrollback clears and printable text the renderer emits, and the test
  asserts the resulting grid and scrollback. Asserting escape sequences would pin the implementation;
  asserting the screen pins what a user sees. This machine has no tmux, so screen capture of a real
  terminal is not the method.
* **The renderer's rules are Pi's, recorded in §11.4**: the 16 ms frame throttle with input
  preempting it, the end-of-line reset, the cursor marker, the five-way redraw decision including the
  Termux height exception, and the refusal to draw a line wider than the terminal.
* **The line-based loop stays as a fallback** for terminals that cannot host the interface, decided
  with the plan. That is pi-go's own requirement (class N) and its slice records why.
* **Fullscreen mode remains possible.** Pi builds its alternate-screen mode on the same component
  model, so choosing the model here does not foreclose it.
* **Revisiting this** means a terminal behaviour Pi has that the line model cannot express. None is
  known: Pi's own fullscreen mode is built on the same model.
