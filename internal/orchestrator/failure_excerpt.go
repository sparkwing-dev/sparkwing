package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Bounds on the failure text a node records. Both the line and byte
// bound apply to the excerpted body -- whichever bites first wins --
// and the headline carries its own, much tighter, byte bound.
//
// The headline bound is not cosmetic: sparkwing.Bash renders the entire
// script into ExecError.Command, so a generated multi-hundred-kilobyte
// script would otherwise land verbatim in the node's error, which is
// exactly the unbounded state row this whole helper exists to prevent.
// With these, boundedFailureText's output has a hard ceiling of roughly
// 5 KiB regardless of input.
const (
	failureExcerptMaxLines = 20
	failureExcerptMaxBytes = 4096
	failureHeadMaxBytes    = 200
)

// boundedFailureText renders err as the text a failed node records in
// its terminal state (node.Error, the node_failed event, the node_end
// log record).
//
// Four things happen here that err.Error() alone does not do:
//
//   - A [sparkwing.ExecError] carries the failed command's raw stderr
//     (or stdout when stderr is empty) inside its message. That output
//     is unbounded -- a failing build can bury the state row under
//     megabytes -- so only its tail is kept, behind a marker pointing at
//     the node log for the rest. The tail is the right end to keep:
//     compilers and linters print their conclusion last.
//   - The headline that identifies the failure is bounded too, because
//     it embeds the command, and a command can be a whole script.
//   - Every byte goes through the run's masker, so a secret that leaked
//     into command output is redacted before it is persisted where
//     `runs status`, the dashboard, and notifications read it.
//   - A plain Go error is bounded from the *front*: with no captured
//     output there is no conclusion at the end, the message itself is
//     the diagnostic, and its first lines are what name the problem.
//     Nothing points at the node log in that case -- the log does not
//     contain the message.
//
// runID and nodeID only build the "where is the rest" pointer; an empty
// pair degrades the marker rather than the excerpt.
func boundedFailureText(ctx context.Context, runID, nodeID string, err error) string {
	if err == nil {
		return ""
	}
	head, body, isOutput := splitFailure(err)
	head = boundedFailureHead(secrets.MaskCtx(ctx, head))
	body = secrets.MaskCtx(ctx, body)

	var b strings.Builder
	if head != "" {
		b.WriteString(head)
	}

	if !isOutput {
		// Plain error: keep the head of the message, not its tail.
		msg, truncated := failureMessageHead(body, failureExcerptMaxLines, failureExcerptMaxBytes)
		if msg == "" {
			return b.String()
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(msg)
		if truncated {
			b.WriteString("\n… (message truncated)")
		}
		return b.String()
	}

	tail, truncated := failureExcerptTail(body, failureExcerptMaxLines, failureExcerptMaxBytes)
	if tail == "" {
		return b.String()
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	if truncated {
		b.WriteString(failureExcerptMarker(runID, nodeID))
		b.WriteString("\n")
	}
	b.WriteString(tail)
	return b.String()
}

// boundedFailureHead trims the failure headline to one short line. The
// headline is "<node>: <wrapping>: command failed (exit N): <command>",
// and <command> is whatever was run -- for sparkwing.Bash, the entire
// script. Keeping the first line up to failureHeadMaxBytes preserves
// the part that identifies the failure (node, step, exit code, the
// start of the command) and drops the part that has no business in a
// state row.
func boundedFailureHead(head string) string {
	if head == "" {
		return ""
	}
	head = normalizeNewlines(head)
	first, rest, multiline := strings.Cut(head, "\n")
	truncated := multiline && strings.TrimSpace(rest) != ""
	if len(first) > failureHeadMaxBytes {
		first = first[:failureHeadMaxBytes]
		for len(first) > 0 && !utf8.RuneStart(first[len(first)-1]) {
			first = first[:len(first)-1]
		}
		// The final rune may itself be incomplete; drop it.
		if r, size := utf8.DecodeLastRuneInString(first); r == utf8.RuneError && size <= 1 {
			first = first[:len(first)-size]
		}
		truncated = true
	}
	first = strings.TrimRight(first, " \t")
	if truncated {
		return first + " … (command truncated)"
	}
	return first
}

// nodeFailureExcerptEvent is the event kind carrying the machine-
// readable half of a node failure. A separate kind rather than a
// richer node_failed payload: node_failed's payload is the error text
// and consumers read it as such, and events are the one node-scoped
// carrier every backend already has (SQLite, controller HTTP, S3), so
// no schema migration is involved.
const nodeFailureExcerptEvent = "node_failure_excerpt"

// failureExcerpt is the structured excerpt a failed node publishes for
// JSON consumers: the masked, bounded output on its own, without the
// headline and marker that decorate the human-facing error text.
//
// The zero value means "no excerpt", which is what a node that failed
// without captured output (a plain Go error) records -- absence is the
// honest answer there, and the node's error still describes the
// failure.
type failureExcerpt struct {
	LogExcerpt string `json:"log_excerpt"`
	Truncated  bool   `json:"log_excerpt_truncated"`
}

// nodeFailureExcerpt extracts the structured excerpt for err, reporting
// ok=false when the error carried no command output to excerpt.
//
// It reads the same split boundedFailureText does, so the two carriers
// cannot diverge: any error whose text is built from captured output
// also publishes that output as data, however the error was wrapped.
func nodeFailureExcerpt(ctx context.Context, err error) (failureExcerpt, bool) {
	if err == nil {
		return failureExcerpt{}, false
	}
	_, body, isOutput := splitFailure(err)
	if !isOutput {
		// No captured output: the whole message is the error, and the
		// error is already recorded. Nothing to excerpt.
		return failureExcerpt{}, false
	}
	tail, truncated := failureExcerptTail(secrets.MaskCtx(ctx, body),
		failureExcerptMaxLines, failureExcerptMaxBytes)
	if tail == "" {
		return failureExcerpt{}, false
	}
	return failureExcerpt{LogExcerpt: tail, Truncated: truncated}, true
}

// appendFailureExcerptEvent publishes err's structured excerpt for a
// directly failed node. A no-op when there is no captured output, so
// only nodes that own their failure ever carry one -- an upstream-
// failed or cancelled node never reaches this path, and nothing here
// reads a log file to invent an excerpt for it.
//
// Best-effort like every other event append: losing the machine-
// readable copy must not change the node's recorded outcome.
func appendFailureExcerptEvent(ctx context.Context, state StateBackend, runID, nodeID string, err error) {
	ex, ok := nodeFailureExcerpt(ctx, err)
	if !ok {
		return
	}
	payload, merr := json.Marshal(ex)
	if merr != nil {
		return
	}
	_ = state.AppendEvent(ctx, runID, nodeID, nodeFailureExcerptEvent, payload)
}

// eventLister is the read side shared by every state backend: the
// local *store.Store, the controller client, the S3 reader, and the
// backend.Backend wrapper all expose exactly this. Keeping the
// dependency this narrow is what lets one excerpt lookup serve both
// local and remote rendering.
type eventLister interface {
	ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error)
}

// excerptPageSize / excerptMaxPages bound the event scan. A run with
// more events than these cover loses excerpts for its tail, never
// correctness: the node error still carries the human-readable
// excerpt.
const (
	excerptPageSize = 1000
	excerptMaxPages = 50
)

// failureExcerptsForRun indexes a run's published excerpts by node id.
// Errors are swallowed: an excerpt is a decoration on an already
// complete failure report, so a backend that cannot serve events must
// degrade to "no excerpt", not to "no failure".
func failureExcerptsForRun(ctx context.Context, src eventLister, runID string) map[string]failureExcerpt {
	if src == nil || runID == "" {
		return nil
	}
	var out map[string]failureExcerpt
	var after int64
	for range excerptMaxPages {
		events, err := src.ListEventsAfter(ctx, runID, after, excerptPageSize)
		if err != nil || len(events) == 0 {
			break
		}
		for _, e := range events {
			after = max(after, e.Seq)
			if e.Kind != nodeFailureExcerptEvent || e.NodeID == "" || len(e.Payload) == 0 {
				continue
			}
			var ex failureExcerpt
			if json.Unmarshal(e.Payload, &ex) != nil || ex.LogExcerpt == "" {
				continue
			}
			if out == nil {
				out = make(map[string]failureExcerpt)
			}
			out[e.NodeID] = ex
		}
		if len(events) < excerptPageSize {
			break
		}
	}
	return out
}

// splitFailure separates err's message into the part that identifies
// the failure (node prefix, wrapping context, the "command failed
// (exit 1): go build ./..." headline) and the body to bound, reporting
// whether that body is captured command output.
//
// isOutput drives everything downstream: captured output is bounded
// from the tail, gets a marker pointing at the node log, and is
// published as a structured excerpt. A plain error's message is bounded
// from the front and published nowhere else.
//
// The output is located in the message rather than assumed to be its
// suffix, so a caller that wrapped the ExecError on both sides --
// fmt.Errorf("%w (attempt 3/3)", execErr) -- still gets its output
// recognized. When the message does not contain the output at all
// (a wrapper rewrote it), the whole message becomes the head, which
// the caller bounds anyway, and the output still comes from the error.
func splitFailure(err error) (head, body string, isOutput bool) {
	full := err.Error()
	var ee *sparkwing.ExecError
	if !errors.As(err, &ee) {
		return "", full, false
	}
	out := strings.TrimSpace(ee.Stderr)
	if out == "" {
		out = strings.TrimSpace(ee.Stdout)
	}
	if out == "" {
		// A terminated or never-started command captured nothing; its
		// message is all there is.
		return "", full, false
	}
	i := strings.LastIndex(full, out)
	if i < 0 {
		return full, out, true
	}
	head = strings.TrimRight(full[:i], "\n")
	if suffix := strings.TrimSpace(full[i+len(out):]); suffix != "" {
		head += " " + suffix
	}
	return head, out, true
}

// normalizeNewlines folds CRLF and lone CR line endings to LF so a
// Windows-y or carriage-return-heavy command does not leave stray \r
// bytes embedded in the persisted text.
func normalizeNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// failureExcerptTail keeps the last maxLines lines of s, then trims the
// result to maxBytes from the end, preferring a line boundary so the
// excerpt never starts mid-line. Reports whether anything was dropped.
//
// Whitespace-only output yields no excerpt at all: a body of blank
// lines is not a diagnostic, and reporting one behind a "see the log"
// marker would send a reader after nothing.
func failureExcerptTail(s string, maxLines, maxBytes int) (string, bool) {
	s = strings.TrimRight(normalizeNewlines(s), "\n")
	if strings.TrimSpace(s) == "" {
		return "", false
	}
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
	if strings.TrimSpace(out) == "" {
		return "", truncated
	}
	return out, truncated
}

// failureMessageHead is failureExcerptTail's counterpart for an error
// message with no captured output: it keeps the *first* maxLines lines
// and maxBytes bytes. A validation error leads with what failed and
// follows with detail, so the front is the payload and the tail is the
// expendable part -- the reverse of command output.
func failureMessageHead(s string, maxLines, maxBytes int) (string, bool) {
	s = strings.TrimRight(normalizeNewlines(s), "\n")
	if strings.TrimSpace(s) == "" {
		return "", false
	}
	truncated := false

	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}
	out := strings.Join(lines, "\n")

	if len(out) > maxBytes {
		out = out[:maxBytes]
		if i := strings.LastIndexByte(out, '\n'); i > 0 {
			out = out[:i]
		} else {
			for len(out) > 0 && !utf8.RuneStart(out[len(out)-1]) {
				out = out[:len(out)-1]
			}
			if r, size := utf8.DecodeLastRuneInString(out); r == utf8.RuneError && size <= 1 {
				out = out[:len(out)-size]
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
