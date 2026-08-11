package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Bounds on the failure text a node records. A failing compiler or
// linter prints its conclusion last, so the tail is what a reader
// wants; the full output stays in the node log. Both bounds apply --
// whichever bites first wins.
const (
	failureExcerptMaxLines = 20
	failureExcerptMaxBytes = 4096
)

// boundedFailureText renders err as the text a failed node records in
// its terminal state (node.Error, the node_failed event, the node_end
// log record).
//
// Three things happen here that err.Error() alone does not do:
//
//   - A [sparkwing.ExecError] carries the failed command's raw stderr
//     (or stdout when stderr is empty) inside its message. That output
//     is unbounded -- a failing build can bury the state row under
//     megabytes -- so only its tail is kept, behind a marker pointing at
//     the node log for the rest.
//   - Every byte goes through the run's masker, so a secret that leaked
//     into command output is redacted before it is persisted where
//     `runs status`, the dashboard, and notifications read it.
//   - A plain Go error keeps its text; it is masked and bounded the same
//     way, which for the short messages this covers is a no-op.
//
// runID and nodeID only build the "where is the rest" pointer; an empty
// pair degrades the marker rather than the excerpt.
func boundedFailureText(ctx context.Context, runID, nodeID string, err error) string {
	if err == nil {
		return ""
	}
	head, body := splitExecErrorOutput(err)
	head = secrets.MaskCtx(ctx, head)
	body = secrets.MaskCtx(ctx, body)

	tail, truncated := failureExcerptTail(body, failureExcerptMaxLines, failureExcerptMaxBytes)

	var b strings.Builder
	if head != "" {
		b.WriteString(head)
		b.WriteString("\n")
	}
	if truncated {
		b.WriteString(failureExcerptMarker(runID, nodeID))
		b.WriteString("\n")
	}
	b.WriteString(tail)
	return b.String()
}

// splitExecErrorOutput separates err's message into the part that
// identifies the failure (node prefix, wrapping context, the "command
// failed (exit 1): go build ./..." headline) and the captured command
// output that follows it. The head is kept whole -- it is short and it
// is the line a reader needs first -- and only the body is bounded.
//
// For anything that is not an ExecError with captured output the whole
// message is the body: nothing is exempt from masking, and a
// multi-line plain error still gets its tail kept rather than its head.
func splitExecErrorOutput(err error) (head, body string) {
	full := err.Error()
	var ee *sparkwing.ExecError
	if !errors.As(err, &ee) {
		return "", full
	}
	out := strings.TrimSpace(ee.Stderr)
	if out == "" {
		out = strings.TrimSpace(ee.Stdout)
	}
	// ExecError.Error() appends the trimmed output after a newline. When
	// that shape does not hold (a terminated or never-started command
	// prints no output at all, or a caller rewrapped the message) treat
	// the whole message as the body.
	if out == "" || !strings.HasSuffix(full, "\n"+out) {
		return "", full
	}
	return strings.TrimSuffix(full, "\n"+out), out
}

// failureExcerptTail keeps the last maxLines lines of s, then trims the
// result to maxBytes from the end, preferring a line boundary so the
// excerpt never starts mid-line. Reports whether anything was dropped.
func failureExcerptTail(s string, maxLines, maxBytes int) (string, bool) {
	s = strings.TrimRight(s, "\n")
	truncated := false

	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
		truncated = true
	}
	out := strings.Join(lines, "\n")

	if len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		} else {
			// One line longer than the byte budget: drop the leading
			// bytes of a split rune so the excerpt stays valid UTF-8.
			for len(out) > 0 && !utf8.RuneStart(out[0]) {
				out = out[1:]
			}
		}
		truncated = true
	}
	return out, truncated
}

// failureExcerptMarker is the line that replaces the dropped output. It
// names the command that prints the rest so the excerpt is a pointer,
// not a dead end.
func failureExcerptMarker(runID, nodeID string) string {
	switch {
	case runID != "" && nodeID != "":
		return fmt.Sprintf("… earlier output omitted (see: sparkwing runs logs --run %s --node %s)", runID, nodeID)
	case runID != "":
		return fmt.Sprintf("… earlier output omitted (see: sparkwing runs logs --run %s)", runID)
	default:
		return "… earlier output omitted (see the node log for the full output)"
	}
}
