package wingd_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// TestStatsReset_ClearsTheEventWindow proves a control client can zero the
// daemon's rolling admission-outcome window: after a grant the window reports
// a run, and after a reset it reports nothing.
func TestStatsReset_ClearsTheEventWindow(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, GraceWindow: -1})

	holder := ensure(t, home, "")
	lease := mustAcquire(t, holder, coreReq("stats-run", 1))
	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	qs := queueOnce(t, home)
	if qs.Events == nil || qs.Events.Runs == 0 {
		t.Fatalf("expected the event window to record the grant, got %+v", qs.Events)
	}

	ctrl := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ctrl.ResetStats(ctx); err != nil {
		t.Fatalf("reset stats: %v", err)
	}

	if qs := queueOnce(t, home); qs.Events != nil {
		t.Fatalf("stats reset did not clear the window: %+v", qs.Events)
	}
}
