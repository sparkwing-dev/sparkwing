package wingd_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// TestCancel_ReattachedHolderIsCancellable proves a holder that reclaims its
// lease after a daemon restart stays cancellable: the successor daemon marks
// the reattached connection finalizable, so a control client's CancelLease
// finds the run and the holder's watch observes the terminal cancel.
func TestCancel_ReattachedHolderIsCancellable(t *testing.T) {
	home := shortHome(t)
	td1 := startDaemon(t, wingd.Config{Home: home, GraceWindow: 300 * time.Millisecond})

	succ := newSuccessor(t, home, "")
	holderCl := spawnClient(t, home, succ)
	lease, err := holderCl.Acquire(context.Background(), coreReq("reattach-cancel", 1), nil)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	cancelled := make(chan wingwire.Cancel, 1)
	go lease.WatchControl(nil, func(c wingwire.Cancel) {
		select {
		case cancelled <- c:
		default:
		}
	})

	td1.stop()
	if err := td1.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon1 exit: %v", err)
	}
	select {
	case <-succ.ready:
	case <-time.After(wingdChurnWait):
		t.Fatal("successor daemon never came up")
	}
	time.Sleep(successorGrace + 500*time.Millisecond)
	waitForHolder(t, home, "reattach-cancel")

	ctrl := ensure(t, home, "")
	found, err := ctrl.CancelLease(context.Background(), "reattach-cancel")
	if err != nil {
		t.Fatalf("cancel lease: %v", err)
	}
	if !found {
		t.Fatal("cancel did not find the reattached holder; it was not marked finalizable")
	}

	select {
	case c := <-cancelled:
		if c.RunID != "reattach-cancel" {
			t.Fatalf("cancel targeted %q, want reattach-cancel", c.RunID)
		}
	case <-time.After(wingdChurnWait):
		t.Fatal("reattached holder never observed the cancel signal")
	}
}
