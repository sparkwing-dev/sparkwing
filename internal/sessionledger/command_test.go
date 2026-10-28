package sessionledger

import (
	"context"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCommandRecordsFromOneOwnerDoNotCollide(t *testing.T) {
	l := OpenWithProbe(filepath.Join(t.TempDir(), "sessions"), &fakeProbe{})
	mk := func(id string) {
		if _, err := l.Record(Record{
			Run: "run-1", Node: "build", OwnerPID: 4242, OwnerBirth: "b",
			Handle: Handle{Kind: "command", ID: id, Argv: []string{"true"}}, Description: "cleanup",
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("aaaa")
	mk("bbbb")
	if got, _ := l.List(); len(got) != 2 {
		t.Fatalf("two command cleanups from one owner collapsed to %d record(s)", len(got))
	}
}

func TestCommandRecordRoundTrips(t *testing.T) {
	l := OpenWithProbe(filepath.Join(t.TempDir(), "sessions"), &fakeProbe{})
	if _, err := l.Record(Record{
		Run: "run-1", Node: "build", OwnerPID: 4242, OwnerBirth: "b",
		Handle: Handle{Kind: "command", ID: "cafe", Argv: []string{"docker", "rm", "-f", "c1"}},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := l.List()
	if len(got) != 1 || got[0].Handle.Kind != "command" || len(got[0].Handle.Argv) != 4 || got[0].Handle.Argv[3] != "c1" {
		t.Fatalf("command record did not round-trip: %+v", got)
	}
}

func TestSweepRunsTheCommandForADeadOwnerThenDropsTheRecord(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "cleaned")
	l := Open(filepath.Join(dir, "sessions")) // real dispatch probe -> runs the argv
	if _, err := l.Record(Record{
		Run: "run-dead", Node: "build", OwnerPID: 999999, OwnerBirth: "gone",
		Handle:      Handle{Kind: "command", ID: "x", Argv: []string{"touch", marker}},
		Description: "touch",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := l.Sweep(context.Background(), SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Verdict != verdictReaped {
		t.Fatalf("outcomes = %+v", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the cleanup command did not run: %v", err)
	}
	if left, _ := l.List(); len(left) != 0 {
		t.Fatalf("record survived a successful cleanup: %+v", left)
	}
}

func TestSweepLeavesACommandWhoseOwnerStillRuns(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "should-not-exist")
	l := Open(filepath.Join(dir, "sessions"))
	birth, err := ownBirth()
	if err != nil {
		t.Skipf("cannot read own birth: %v", err)
	}
	if _, err := l.Record(Record{
		Run: "run-live", Node: "build", OwnerPID: os.Getpid(), OwnerBirth: birth,
		Handle: Handle{Kind: "command", ID: "x", Argv: []string{"touch", marker}},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := l.Sweep(context.Background(), SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Verdict != VerdictLive {
		t.Fatalf("outcomes = %+v", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a live owner's cleanup command was run")
	}
}

func TestSweepDropsAFailingCommandAfterOneAttempt(t *testing.T) {
	dir := t.TempDir()
	l := Open(filepath.Join(dir, "sessions"))
	if _, err := l.Record(Record{
		Run: "run-dead", Node: "build", OwnerPID: 999999, OwnerBirth: "gone",
		Handle:      Handle{Kind: "command", ID: "x", Argv: []string{"false"}},
		Description: "false",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := l.Sweep(context.Background(), SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Verdict != verdictFailed || out[0].Err == nil {
		t.Fatalf("outcomes = %+v", out)
	}
	// best-effort: the record is dropped after the single attempt, not retried forever
	if left, _ := l.List(); len(left) != 0 {
		t.Fatalf("a best-effort command was kept for retry: %+v", left)
	}
}

type eventRecorder struct{ payloads []string }

func (r *eventRecorder) AppendEvent(_ context.Context, _, _, _ string, payload []byte) error {
	r.payloads = append(r.payloads, string(payload))
	return nil
}

// A failed cleanup's argv and output can quote a secret no sweeping process
// can mask, so the reaped event uses a safe label and the exit status.
func TestAFailedCleanupEventQuotesNeitherArgvNorOutput(t *testing.T) {
	const secret = "hunter2-cleanup-secret"
	l := Open(filepath.Join(t.TempDir(), "sessions"))
	if _, err := l.Record(Record{
		Run: "run-dead", Node: "build", OwnerPID: 999999, OwnerBirth: "gone",
		Handle:      Handle{Kind: "command", ID: "x", Argv: []string{"sh", "-c", "echo " + secret + "; exit 3", secret}},
		Description: "sh",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := l.Sweep(context.Background(), SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Verdict != verdictFailed {
		t.Fatalf("outcomes = %+v, want one failed cleanup", out)
	}
	rec := &eventRecorder{}
	if errs := RecordOutcomes(context.Background(), rec, "test", out); len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(rec.payloads) != 1 || !strings.Contains(rec.payloads[0], "exit status 3") || strings.Contains(rec.payloads[0], secret) {
		t.Fatalf("reaped events = %q, want the exit without the secret", rec.payloads)
	}
}

// A missing executable can have secrets in any part of its path.
func TestACleanupThatCannotStartOmitsExecutableText(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "hunter2-secret-dir", "missing-hunter2-cleanup")
	err := runCleanupCommand(context.Background(), []string{missing, "arg"})
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("err = %v, want an error without executable text", err)
	}
}

// The ledger keeps a cleanup's argv, which a sweep replays, so the record is
// readable by its owner alone.
func TestARecordIsPrivateToItsOwner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	if _, err := Open(root).Record(Record{
		Run: "run-1", Node: "build", OwnerPID: 4242, OwnerBirth: "b",
		Handle: Handle{Kind: "command", ID: "x", Argv: []string{"vault", "revoke", "hunter2"}},
	}); err != nil {
		t.Fatal(err)
	}
	recs, err := Open(root).List()
	if err != nil || len(recs) != 1 {
		t.Fatalf("records = %+v (err %v)", recs, err)
	}
	for path, want := range map[string]os.FileMode{recs[0].filename(): 0o600, filepath.Dir(recs[0].filename()): 0o700, root: 0o700} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v (err %v), want %v", path, fi.Mode().Perm(), err, want)
		}
	}
}

func TestStraySessionEventKindIsARunEventKind(t *testing.T) {
	if !slices.Contains(runstate.RunEventKinds, eventKind) {
		t.Fatalf("%q is not in runstate.RunEventKinds, so the console never hears it", eventKind)
	}
}
