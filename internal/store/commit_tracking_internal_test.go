package store

import "testing"

// Cloud never waits on commits, so its store must not scan every query for
// writes or take the wake mutex on every commit.
func TestCommitTrackingStaysOffUntilSomeoneWaits(t *testing.T) {
	s := newTestStore(t)
	const write = "UPDATE settings SET value=? WHERE key=? RETURNING value"
	tx := &storeTx{store: s}
	tx.noteReturningWrite(write)
	if tx.wrote {
		t.Fatal("a store nobody waits on scanned a query for writes")
	}
	_ = s.NextCommit()
	tx.noteReturningWrite(write)
	if !tx.wrote {
		t.Fatal("a store with a waiter missed a RETURNING write")
	}
	if allocs := testing.AllocsPerRun(100, func() { tx.noteReturningWrite(write) }); allocs != 0 {
		t.Fatalf("write detection allocated %.0f times per query", allocs)
	}
}
