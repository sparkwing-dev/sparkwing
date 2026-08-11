package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
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
