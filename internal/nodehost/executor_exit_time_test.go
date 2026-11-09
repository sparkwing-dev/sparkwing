package nodehost

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

type exitObservationProcess struct {
	ExecutorProcess
	beforeCleanup func()
}

func (p exitObservationProcess) Cleanup(ctx context.Context) error {
	p.beforeCleanup()
	return p.ExecutorProcess.Cleanup(ctx)
}

func TestNodeExecutorReportsExitBeforePublication(t *testing.T) {
	input, st, deps, processes := executorFixture(t)
	var publicationAt, cleanupAt time.Time
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, in, activity)
		return exitObservationProcess{ExecutorProcess: process, beforeCleanup: func() {
			cleanupAt = time.Now().UTC()
			if in.exitedAt == nil || in.exitedAt.IsZero() || in.exitedAt.After(cleanupAt) {
				t.Fatal("process cleanup lost observed exit")
			}
		}}, err
	}
	deps.PrepareCaches = func(context.Context, NodeInput, nodewire.JobSpec) (ExecutorFinalizer, error) {
		return func(_ context.Context, _ NodeInput, _ *runstate.NodeResult) error {
			if len(*processes) != 1 || (*processes)[0].closed != 1 {
				t.Fatal("publication began before execution process joined")
			}
			publicationAt = time.Now().UTC()
			return nil
		}, nil
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	report := st.changes[len(st.changes)-1]
	if report.Cause != runstate.CauseReport || report.Extra.ExitedAt == nil || report.Extra.ExitedAt.Before(before) || report.Extra.ExitedAt.After(publicationAt) || report.Extra.ExitedAt.After(cleanupAt) {
		t.Fatalf("reported exit = %v, want between %v and publication %v", report.Extra.ExitedAt, before, publicationAt)
	}
}
