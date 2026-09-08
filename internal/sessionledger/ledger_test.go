package sessionledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeProbe struct {
	alive      map[int]bool
	terminated []Handle
	failKill   error
}

func (f *fakeProbe) OwnerAlive(rec Record) (bool, error) { return f.alive[rec.OwnerPID], nil }

func (f *fakeProbe) Terminate(_ context.Context, h Handle) error {
	if f.failKill != nil {
		return f.failKill
	}
	f.terminated = append(f.terminated, h)
	return nil
}

func record(t *testing.T, l *Ledger, run, node string, owner int) func() {
	t.Helper()
	release, err := l.Record(Record{
		Run: run, Node: node, OwnerPID: owner, OwnerBirth: "b",
		Handle:  Handle{Kind: "session", LeaderPID: owner + 100, SessionID: owner + 100, LeaderBirth: "lb"},
		Command: "go build ./...",
	})
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func TestRecordIsListedUntilReleased(t *testing.T) {
	l := OpenWithProbe(filepath.Join(t.TempDir(), "sessions"), &fakeProbe{})
	release := record(t, l, "run-1", "build", 4242)
	got, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Run != "run-1" || got[0].Node != "build" || got[0].Handle.LeaderPID != 4342 {
		t.Fatalf("records = %+v", got)
	}
	release()
	got, err = l.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("released record still listed: %+v", got)
	}
}

func TestRecordRefusesWhatItCannotSweep(t *testing.T) {
	l := OpenWithProbe(t.TempDir(), &fakeProbe{})
	if _, err := l.Record(Record{Run: "r", Node: "n", OwnerPID: 1}); err == nil {
		t.Fatal("recorded init as an owner")
	}
	if _, err := l.Record(Record{Run: "", Node: "n", OwnerPID: 5}); err == nil {
		t.Fatal("recorded a session with no run")
	}
}

func TestListSkipsFilesThatAreNotRecords(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	l := OpenWithProbe(dir, &fakeProbe{})
	record(t, l, "run-1", "build", 4242)
	if err := os.WriteFile(filepath.Join(dir, "run-1", "build", "junk.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("records = %d, want the one real record", len(got))
	}
}

func TestSweepEndsOnlySessionsWhoseOwnerIsGone(t *testing.T) {
	probe := &fakeProbe{alive: map[int]bool{1000: true}}
	l := OpenWithProbe(filepath.Join(t.TempDir(), "sessions"), probe)
	record(t, l, "run-live", "build", 1000)
	record(t, l, "run-dead", "build", 2000)
	record(t, l, "run-dead", "test", 2001)

	out, err := l.Sweep(context.Background(), SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	verdicts := map[string]Verdict{}
	for _, o := range out {
		verdicts[o.Record.Run+"/"+o.Record.Node] = o.Verdict
	}
	want := map[string]Verdict{"run-live/build": VerdictLive, "run-dead/build": VerdictReaped, "run-dead/test": VerdictReaped}
	for k, v := range want {
		if verdicts[k] != v {
			t.Errorf("%s = %s, want %s", k, verdicts[k], v)
		}
	}
	if len(probe.terminated) != 2 {
		t.Fatalf("terminated %d handles, want 2", len(probe.terminated))
	}
	left, _ := l.List()
	if len(left) != 1 || left[0].Run != "run-live" {
		t.Fatalf("records after sweep = %+v, want only the live one", left)
	}
}

func TestSweepScopedToARunLeavesOtherRunsAlone(t *testing.T) {
	probe := &fakeProbe{}
	l := OpenWithProbe(filepath.Join(t.TempDir(), "sessions"), probe)
	record(t, l, "run-a", "build", 2000)
	record(t, l, "run-b", "build", 2001)
	out, err := l.Sweep(context.Background(), SweepOptions{Run: "run-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Record.Run != "run-a" || out[0].Verdict != VerdictReaped {
		t.Fatalf("outcomes = %+v", out)
	}
	left, _ := l.List()
	if len(left) != 1 || left[0].Run != "run-b" {
		t.Fatalf("records after scoped sweep = %+v", left)
	}
}

func TestDryRunReportsWithoutKillingOrRemoving(t *testing.T) {
	probe := &fakeProbe{}
	l := OpenWithProbe(filepath.Join(t.TempDir(), "sessions"), probe)
	record(t, l, "run-dead", "build", 2000)
	out, err := l.Sweep(context.Background(), SweepOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Verdict != VerdictWouldReap {
		t.Fatalf("outcomes = %+v", out)
	}
	if len(probe.terminated) != 0 {
		t.Fatal("dry run terminated a session")
	}
	if left, _ := l.List(); len(left) != 1 {
		t.Fatal("dry run removed the record")
	}
}

func TestSweepKeepsARecordItCouldNotEnd(t *testing.T) {
	probe := &fakeProbe{failKill: errors.New("stuck in D state")}
	l := OpenWithProbe(filepath.Join(t.TempDir(), "sessions"), probe)
	record(t, l, "run-dead", "build", 2000)
	out, err := l.Sweep(context.Background(), SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Verdict != VerdictFailed || out[0].Err == nil {
		t.Fatalf("outcomes = %+v", out)
	}
	if left, _ := l.List(); len(left) != 1 {
		t.Fatal("a session that could not be ended lost its record")
	}
}

func TestSweepOfAMissingLedgerIsEmpty(t *testing.T) {
	l := OpenWithProbe(filepath.Join(t.TempDir(), "never-created"), &fakeProbe{})
	out, err := l.Sweep(context.Background(), SweepOptions{})
	if err != nil || len(out) != 0 {
		t.Fatalf("sweep = %+v, %v", out, err)
	}
}
