//go:build windows

package sparkwing

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
)

func TestExec_CancelTerminatesTheStepJobAndClearsItsRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_RUN_ID", "run-w")
	t.Setenv("SPARKWING_NODE_ID", "build")
	p := paths.PathsAt(home)
	ledger := sessionledger.Open(p.SessionLedgerDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = execCmd(ctx, "cmd", []string{"/c", `start /b ping -n 60 127.0.0.1 >nul & ping -n 60 127.0.0.1 >nul`}, t.TempDir(), nil)
	}()

	var jobName string
	deadline := time.Now().Add(10 * time.Second)
	for jobName == "" {
		if time.Now().After(deadline) {
			t.Fatal("the running step was never recorded")
		}
		recs, _ := ledger.List()
		if len(recs) == 1 {
			jobName = recs[0].Handle.JobName
		}
		time.Sleep(20 * time.Millisecond)
	}
	if jobName == "" {
		t.Fatal("record carries no job name")
	}

	cancel()
	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled step did not return")
	}
	if recs, _ := ledger.List(); len(recs) != 0 {
		t.Fatalf("records after the step returned: %+v", recs)
	}
	if err := procgroup.TerminateJobByName(jobName); err != nil {
		t.Fatalf("job %s still reachable after the step returned: %v", jobName, err)
	}
}
