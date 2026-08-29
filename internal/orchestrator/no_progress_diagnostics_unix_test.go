//go:build unix

package orchestrator_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func init() {
	register("mod-no-progress-diagnostic", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &noProgressDiagnosticPipeline{}
	})
}

type noProgressDiagnosticPipeline struct{ sparkwing.Base }

func (noProgressDiagnosticPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "silent", func(ctx context.Context) error {
		_, err := sparkwing.Exec(ctx, os.Args[0], "-test.run=^TestNoProgressDiagnosticHelper$").
			Env("SPARKWING_NO_PROGRESS_DIAGNOSTIC_HELPER", "1").
			Run()
		return err
	}).NoProgressTimeout(100 * time.Millisecond)
	return nil
}

func TestNoProgressTimeout_DumpIsAvailableThroughJobLogs(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-no-progress-diagnostic"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	nodes, err := st.ListNodes(context.Background(), res.RunID)
	_ = st.Close()
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].FailureReason != store.FailureNoProgressTimeout {
		t.Fatalf("nodes = %+v, want a no-progress timeout", nodes)
	}

	var logs bytes.Buffer
	if err := orchestrator.JobLogs(context.Background(), p, res.RunID, orchestrator.LogsOpts{Node: "silent"}, &logs); err != nil {
		t.Fatalf("JobLogs: %v", err)
	}
	got := logs.String()
	for _, want := range []string{"SIGQUIT: quit", "goroutine", "TestNoProgressDiagnosticHelper"} {
		if !strings.Contains(got, want) {
			t.Fatalf("client-visible node log omitted %q", want)
		}
	}
}

func TestNoProgressDiagnosticHelper(t *testing.T) {
	if os.Getenv("SPARKWING_NO_PROGRESS_DIAGNOSTIC_HELPER") != "1" {
		return
	}
	blocked := make(chan struct{})
	for range 100 {
		go func() { <-blocked }()
	}
	fmt.Fprintln(os.Stderr, "diagnostic helper ready")
	for {
		time.Sleep(time.Hour)
	}
}
