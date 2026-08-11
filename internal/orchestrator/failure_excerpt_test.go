package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// execErrorWithStderr builds the error shape a failing sparkwing.Bash
// step produces: an ExecError whose message embeds the raw stderr,
// wrapped by the step and node layers above it.
func execErrorWithStderr(stderr string) error {
	return fmt.Errorf("build: %w", &sparkwing.ExecError{
		Command:  "go build ./...",
		Stderr:   stderr,
		ExitCode: 1,
	})
}

func lines(n int, format string) string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf(format, i+1)
	}
	return strings.Join(out, "\n")
}

// A failing build prints its conclusion last, so the excerpt keeps the
// end of the output, drops the rest behind a marker, and the marker
// names the command that prints the whole thing.
func TestBoundedFailureText_ExecErrorKeepsMarkedTail(t *testing.T) {
	err := execErrorWithStderr(lines(500, "compile error on file %d"))
	got := boundedFailureText(context.Background(), "run-1", "build", err)

	if !strings.Contains(got, "command failed (exit 1): go build ./...") {
		t.Fatalf("excerpt dropped the failure headline:\n%s", got)
	}
	if !strings.Contains(got, "compile error on file 500") {
		t.Fatalf("excerpt dropped the last line of output:\n%s", got)
	}
	if strings.Contains(got, "compile error on file 1\n") {
		t.Fatalf("excerpt kept the head of the output instead of the tail:\n%s", got)
	}
	if !strings.Contains(got, "sparkwing runs logs --run run-1 --node build") {
		t.Fatalf("excerpt omits the pointer to the full output:\n%s", got)
	}
	if n := strings.Count(got, "\n") + 1; n > failureExcerptMaxLines+2 {
		t.Fatalf("excerpt is %d lines, want at most %d (tail + headline + marker)",
			n, failureExcerptMaxLines+2)
	}
}

// The stdout fallback matters for tools that report on stdout and exit
// non-zero without writing to stderr at all.
func TestBoundedFailureText_FallsBackToStdout(t *testing.T) {
	err := fmt.Errorf("lint: %w", &sparkwing.ExecError{
		Command:  "golangci-lint run",
		Stdout:   lines(100, "issue %d"),
		ExitCode: 1,
	})
	got := boundedFailureText(context.Background(), "run-1", "lint", err)
	if !strings.Contains(got, "issue 100") {
		t.Fatalf("stdout tail missing:\n%s", got)
	}
	if !strings.Contains(got, "earlier output omitted") {
		t.Fatalf("truncated stdout must carry the marker:\n%s", got)
	}
}

// Command output is a common place for a secret to surface (a curl
// that echoes its own URL, an env dump on failure). The excerpt is
// persisted where `runs status` and the dashboard read it, so it goes
// through the run's masker first.
func TestBoundedFailureText_MasksSecretsInOutput(t *testing.T) {
	const secret = "hunter2-deploy-token"
	m := secrets.NewMasker()
	m.Register(secret)
	ctx := secrets.WithMasker(context.Background(), m)

	err := execErrorWithStderr("auth failed for token " + secret)
	got := boundedFailureText(ctx, "run-1", "deploy", err)
	if strings.Contains(got, secret) {
		t.Fatalf("excerpt leaked the secret value:\n%s", got)
	}
	if !strings.Contains(got, "auth failed for token ***") {
		t.Fatalf("excerpt missing the redacted line:\n%s", got)
	}
}

// A plain Go error is the common case and must round-trip untouched:
// no marker, no reformatting, byte-identical to err.Error().
func TestBoundedFailureText_PlainErrorUnchanged(t *testing.T) {
	err := fmt.Errorf("deploy: %w", errors.New("no such cluster \"prod-2\""))
	got := boundedFailureText(context.Background(), "run-1", "deploy", err)
	if got != err.Error() {
		t.Fatalf("plain error rewritten:\n got %q\nwant %q", got, err.Error())
	}
	if boundedFailureText(context.Background(), "run-1", "deploy", nil) != "" {
		t.Fatal("nil error must render as empty text")
	}
}

// Both bounds are live: 20 lines is not a licence for 20 megabytes.
func TestBoundedFailureText_ByteBoundBitesBeforeLineBound(t *testing.T) {
	wide := make([]string, 10)
	for i := range wide {
		wide[i] = fmt.Sprintf("%04d-%s", i+1, strings.Repeat("x", 1000))
	}
	err := execErrorWithStderr(strings.Join(wide, "\n"))
	got := boundedFailureText(context.Background(), "run-1", "build", err)

	if len(got) > failureExcerptMaxBytes+512 {
		t.Fatalf("excerpt is %d bytes, want the %d-byte bound to hold (headline + marker aside)",
			len(got), failureExcerptMaxBytes)
	}
	if !strings.Contains(got, "0010-") {
		t.Fatalf("byte-bounded excerpt dropped the last line:\n%s", got[:200])
	}
	if strings.Contains(got, "0001-") {
		t.Fatal("byte bound did not drop the earliest lines")
	}
	if !strings.Contains(got, "earlier output omitted") {
		t.Fatal("byte-bounded excerpt must carry the marker")
	}
}

// A minified bundler error or a base64 blob arrives as one enormous
// line: the byte bound still holds, and the cut leaves valid UTF-8.
func TestBoundedFailureText_SingleHugeLineStaysValidUTF8(t *testing.T) {
	err := execErrorWithStderr(strings.Repeat("é", 8000))
	got := boundedFailureText(context.Background(), "run-1", "bundle", err)
	if len(got) > failureExcerptMaxBytes+512 {
		t.Fatalf("excerpt is %d bytes, want at most ~%d", len(got), failureExcerptMaxBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("byte-bounded excerpt is not valid UTF-8")
	}
}

// Output that already fits stays whole -- no marker, no lost bytes.
func TestBoundedFailureText_ShortOutputKeptWhole(t *testing.T) {
	err := execErrorWithStderr("undefined: Foo\nexit status 2")
	got := boundedFailureText(context.Background(), "run-1", "build", err)
	if got != err.Error() {
		t.Fatalf("short output rewritten:\n got %q\nwant %q", got, err.Error())
	}
	if strings.Contains(got, "earlier output omitted") {
		t.Fatal("untruncated output must not carry the marker")
	}
}

// sparkwing.Bash renders the whole script into ExecError.Command, so
// the headline is attacker-sized input, not a label. A generated
// several-hundred-kilobyte script must not reach the state row.
func TestBoundedFailureText_HugeCommandIsBounded(t *testing.T) {
	script := "set -euo pipefail\n" + strings.Repeat("echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n", 8000)
	if len(script) < 200_000 {
		t.Fatalf("test fixture too small: %d bytes", len(script))
	}
	err := fmt.Errorf("build: step %q: %w", "compile", &sparkwing.ExecError{
		Command:  script,
		Stderr:   lines(500, "compile error on file %d"),
		ExitCode: 2,
	})

	got := boundedFailureText(context.Background(), "run-1", "build", err)
	if len(got) > 5*1024 {
		t.Fatalf("failure text is %d bytes; the ceiling must hold regardless of command size", len(got))
	}
	if !strings.Contains(got, "build: step \"compile\": command failed (exit 2)") {
		t.Fatalf("bounded headline lost the identifying prefix:\n%s", got[:min(len(got), 400)])
	}
	if !strings.Contains(got, "… (command truncated)") {
		t.Fatalf("a truncated command must say so:\n%s", got[:min(len(got), 400)])
	}
	if !strings.Contains(got, "compile error on file 500") {
		t.Fatalf("bounding the head must not cost the output tail:\n%s", got)
	}
	head, _, _ := strings.Cut(got, "\n")
	if len(head) > failureHeadMaxBytes+len(" … (command truncated)") {
		t.Fatalf("headline is %d bytes, want <= %d", len(head), failureHeadMaxBytes)
	}
}

// A single-line command that is merely long is truncated the same way,
// and one that fits is left alone.
func TestBoundedFailureHead_BoundsAndPreserves(t *testing.T) {
	short := "deploy: command failed (exit 1): kubectl apply -f app.yaml"
	if got := boundedFailureHead(short); got != short {
		t.Fatalf("short headline rewritten: %q", got)
	}
	long := "deploy: command failed (exit 1): " + strings.Repeat("x", 500)
	got := boundedFailureHead(long)
	if !strings.HasSuffix(got, "… (command truncated)") {
		t.Fatalf("long headline missing the marker: %q", got)
	}
	if !strings.HasPrefix(got, "deploy: command failed (exit 1): x") {
		t.Fatalf("long headline lost its prefix: %q", got)
	}
	if len(got) > failureHeadMaxBytes+len(" … (command truncated)") {
		t.Fatalf("bounded headline is %d bytes", len(got))
	}
	if !utf8.ValidString(boundedFailureHead("deploy: " + strings.Repeat("é", 300))) {
		t.Fatal("headline truncation produced invalid UTF-8")
	}
}

// A plain error has no conclusion at the end: the message leads with
// what failed, so the front is what must survive -- and nothing points
// at a log that does not contain the message.
func TestBoundedFailureText_PlainErrorKeepsHead(t *testing.T) {
	body := "config validation failed for cluster prod-2 (3 fatal problems)\n" +
		lines(40, "  - problem %d: field is required")
	err := fmt.Errorf("validate: %w", errors.New(body))

	got := boundedFailureText(context.Background(), "run-1", "validate", err)
	if !strings.HasPrefix(got, "validate: config validation failed for cluster prod-2 (3 fatal problems)") {
		t.Fatalf("plain error lost its head:\n%s", got)
	}
	if strings.Contains(got, "problem 40") {
		t.Fatalf("plain error should drop its tail, not its head:\n%s", got)
	}
	if strings.Contains(got, "runs logs") || strings.Contains(got, "earlier output omitted") {
		t.Fatalf("a plain error must not point at logs that lack the message:\n%s", got)
	}
	if !strings.HasSuffix(got, "… (message truncated)") {
		t.Fatalf("a truncated message must say so:\n%s", got)
	}
	if n := strings.Count(got, "\n") + 1; n > failureExcerptMaxLines+2 {
		t.Fatalf("plain error text is %d lines", n)
	}
}

// The text and the structured excerpt read the same split, so a
// wrapper that appends after the output cannot leave one carrier with
// an excerpt and the other without.
func TestNodeFailureExcerpt_SurvivesSuffixWrapping(t *testing.T) {
	inner := &sparkwing.ExecError{
		Command:  "go test ./...",
		Stderr:   lines(100, "FAIL pkg/%d"),
		ExitCode: 1,
	}
	for name, err := range map[string]error{
		"prefix wrap": fmt.Errorf("test: %w", inner),
		"suffix wrap": fmt.Errorf("%w (attempt 3/3)", inner),
	} {
		ex, ok := nodeFailureExcerpt(context.Background(), err)
		if !ok {
			t.Fatalf("%s: no structured excerpt published", name)
		}
		if !strings.Contains(ex.LogExcerpt, "FAIL pkg/100") || !ex.Truncated {
			t.Fatalf("%s: excerpt = %q (truncated=%v)", name, ex.LogExcerpt, ex.Truncated)
		}
		text := boundedFailureText(context.Background(), "run-1", "test", err)
		if !strings.Contains(text, "FAIL pkg/100") {
			t.Fatalf("%s: text and excerpt disagree:\n%s", name, text)
		}
	}
	if _, ok := nodeFailureExcerpt(context.Background(), errors.New("boom")); ok {
		t.Fatal("a plain error must publish no excerpt")
	}
}

// CRLF output must not leave stray carriage returns in the persisted
// text, and output that is only whitespace is not a diagnostic.
func TestBoundedFailureText_NormalizesAndDropsBlankOutput(t *testing.T) {
	crlf := execErrorWithStderr("first line\r\nsecond line\r\n")
	got := boundedFailureText(context.Background(), "run-1", "build", crlf)
	if strings.Contains(got, "\r") {
		t.Fatalf("carriage returns survived into the failure text: %q", got)
	}
	if !strings.HasSuffix(got, "second line") {
		t.Fatalf("CRLF output mangled: %q", got)
	}

	blank := execErrorWithStderr("   \n\n \t \n")
	got = boundedFailureText(context.Background(), "run-1", "build", blank)
	if strings.Contains(got, "earlier output omitted") {
		t.Fatalf("blank output must not get a marker: %q", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Fatalf("blank output left trailing newlines: %q", got)
	}
	if _, ok := nodeFailureExcerpt(context.Background(), blank); ok {
		t.Fatal("blank output must publish no excerpt")
	}
}

// countingEvents is an eventLister over a fixed event list that
// records how many pages were requested.
type countingEvents struct {
	events []store.Event
	calls  int
	err    error
}

func (c *countingEvents) ListEventsAfter(_ context.Context, _ string, after int64, limit int) ([]store.Event, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	var out []store.Event
	for _, e := range c.events {
		if e.Seq > after {
			out = append(out, e)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func excerptEvent(seq int64, nodeID string) store.Event {
	return store.Event{
		Seq: seq, NodeID: nodeID, Kind: nodeFailureExcerptEvent,
		Payload: []byte(`{"log_excerpt":"boom","log_excerpt_truncated":false}`),
	}
}

func filler(from, to int64) []store.Event {
	var out []store.Event
	for seq := from; seq <= to; seq++ {
		out = append(out, store.Event{Seq: seq, NodeID: "n", Kind: "node_started"})
	}
	return out
}

// Scanning a run's event stream is a paged HTTP call on a controller
// profile. A run with no failures must not pay for one, and a scan that
// has found every excerpt it wants must stop.
func TestFailureExcerptsFor_ScansOnlyWhenItMustAndStopsEarly(t *testing.T) {
	src := &countingEvents{events: filler(1, 5000)}
	ix := failureExcerptsFor(context.Background(), src, "run-1", nil)
	if src.calls != 0 {
		t.Fatalf("a run with no failed nodes made %d event calls, want 0", src.calls)
	}
	if !ix.Complete || ix.Unavailable("anything") {
		t.Fatal("an empty lookup is vacuously complete")
	}

	early := append([]store.Event{excerptEvent(2, "build")}, filler(3, 5000)...)
	src = &countingEvents{events: early}
	ix = failureExcerptsFor(context.Background(), src, "run-1", map[string]struct{}{"build": {}})
	if src.calls != 1 {
		t.Fatalf("scan made %d calls after finding every excerpt in page 1, want 1", src.calls)
	}
	if ex, ok := ix.Get("build"); !ok || ex.LogExcerpt != "boom" {
		t.Fatalf("excerpt not found: %+v", ix)
	}
	if !ix.Complete {
		t.Fatal("a scan that found everything is complete")
	}
}

// Absence has to be earned. When the scan runs out of budget, or the
// backend refuses, a missing excerpt is reported as unavailable rather
// than as "this node published none".
func TestFailureExcerptsFor_IncompleteScanReportsUnavailable(t *testing.T) {
	want := map[string]struct{}{"build": {}}

	huge := &countingEvents{events: filler(1, excerptPageSize*excerptMaxPages+excerptPageSize)}
	ix := failureExcerptsFor(context.Background(), huge, "run-1", want)
	if ix.Complete {
		t.Fatal("a scan that hit the page cap must not claim completeness")
	}
	if !ix.Unavailable("build") {
		t.Fatal("a failed node the scan never reached is excerpt-unavailable")
	}
	if huge.calls != excerptMaxPages {
		t.Fatalf("scan made %d pages, want the %d-page cap", huge.calls, excerptMaxPages)
	}

	broken := &countingEvents{err: errors.New("controller down")}
	ix = failureExcerptsFor(context.Background(), broken, "run-1", want)
	if ix.Complete || !ix.Unavailable("build") {
		t.Fatal("a backend that cannot serve events yields unavailable, not absent")
	}

	// A completed scan that simply found nothing is authoritative.
	quiet := &countingEvents{events: filler(1, 10)}
	ix = failureExcerptsFor(context.Background(), quiet, "run-1", want)
	if !ix.Complete || ix.Unavailable("build") {
		t.Fatal("a completed scan reports authoritative absence")
	}
}

// The two renderers agree on how the three states look.
func TestFailedNodeReports_MarksUnavailableNotAbsent(t *testing.T) {
	nodes := []*store.Node{
		{NodeID: "build", Outcome: "failed", Error: "boom"},
		{NodeID: "deploy", Outcome: "cancelled", Error: "upstream-failed"},
	}

	rows := failedNodeReports(nodes, failureExcerptIndex{Complete: false})
	if len(rows) != 1 || !rows[0].LogExcerptUnavailable || rows[0].LogExcerpt != "" {
		t.Fatalf("incomplete index should mark the failure unavailable: %+v", rows)
	}
	rows = failedNodeReports(nodes, failureExcerptIndex{Complete: true})
	if len(rows) != 1 || rows[0].LogExcerptUnavailable {
		t.Fatalf("complete index should report plain absence: %+v", rows)
	}

	wrapped := withFailureExcerpts([]nodeWithSteps{
		{Node: nodes[0]}, {Node: nodes[1]},
	}, failureExcerptIndex{Complete: false})
	if !wrapped[0].LogExcerptUnavailable {
		t.Fatalf("failed node should be marked unavailable: %+v", wrapped[0])
	}
	if wrapped[1].LogExcerptUnavailable {
		t.Fatalf("a cancelled node never had an excerpt to lose: %+v", wrapped[1])
	}
}

// Without a run/node pair the marker degrades to a generic pointer
// rather than emitting a command a reader cannot run.
func TestFailureExcerptMarker_DegradesWithoutIDs(t *testing.T) {
	if got := failureExcerptMarker("", ""); strings.Contains(got, "--run") {
		t.Fatalf("marker with no ids must not name a command: %q", got)
	}
	if got := failureExcerptMarker("run-1", ""); !strings.Contains(got, "--run run-1") || strings.Contains(got, "--node") {
		t.Fatalf("marker with only a run id: %q", got)
	}
}
