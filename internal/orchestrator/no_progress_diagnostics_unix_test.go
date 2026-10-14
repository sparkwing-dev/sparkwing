//go:build linux || darwin

package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

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
		// safety: the window must outlast a cold child start, or SIGQUIT lands before the Go runtime can dump
	}).NoProgressTimeout(2 * time.Second)
	return nil
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
