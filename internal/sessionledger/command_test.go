package sessionledger

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCommandRecordsFromOneOwnerDoNotCollide(t *testing.T) {
	l := OpenWithProbe(filepath.Join(t.TempDir(), "sessions"), &fakeProbe{})
	mk := func(id string) {
		if _, err := l.Record(Record{
			Run: "run-1", Node: "build", OwnerPID: 4242, OwnerBirth: "b",
			Handle: Handle{Kind: "command", ID: id, Argv: []string{"true"}}, Command: "cleanup",
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
		Handle:  Handle{Kind: "command", ID: "x", Argv: []string{"touch", marker}},
		Command: "touch marker",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := l.Sweep(context.Background(), SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Verdict != VerdictReaped {
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
		Handle:  Handle{Kind: "command", ID: "x", Argv: []string{"false"}},
		Command: "always fails",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := l.Sweep(context.Background(), SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Verdict != VerdictFailed || out[0].Err == nil {
		t.Fatalf("outcomes = %+v", out)
	}
	// best-effort: the record is dropped after the single attempt, not retried forever
	if left, _ := l.List(); len(left) != 0 {
		t.Fatalf("a best-effort command was kept for retry: %+v", left)
	}
}
