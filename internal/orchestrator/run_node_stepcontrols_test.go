package orchestrator_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var stepControlsApplied struct {
	mu sync.Mutex
	n  int
}

type stepControlsPipe struct{ sparkwing.Base }

func (stepControlsPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "apply", func(context.Context) error {
		stepControlsApplied.mu.Lock()
		defer stepControlsApplied.mu.Unlock()
		stepControlsApplied.n++
		return nil
	})
	return nil
}

var stepControlsOnce sync.Once

func registerStepControlsPipe() {
	stepControlsOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("pod-step-controls",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepControlsPipe{} })
	})
}

func TestRunNodeOnce_IgnoresAmbientStepControlsOffTheCoordinatedPath(t *testing.T) {
	registerStepControlsPipe()
	isolateCheckout(t)
	isolateProfiles(t)
	t.Setenv("SPARKWING_DRY_RUN", "1")
	t.Setenv("SPARKWING_START_AT", "a-step-this-pipeline-never-declared")

	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	p := orchestrator.PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).Handler())
	defer srv.Close()

	ctx := context.Background()
	const (
		runID  = "run-step-controls"
		nodeID = "apply"
	)
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "pod-step-controls", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	stepControlsApplied.mu.Lock()
	stepControlsApplied.n = 0
	stepControlsApplied.mu.Unlock()

	res, err := orchestrator.RunNodeOnce(ctx, srv.URL, "", runID, nodeID,
		"pod:"+runID+":"+nodeID, "", &captureLogger{}, quiet, nil)
	if err != nil {
		t.Fatalf("RunNodeOnce: %v (an ambient SPARKWING_START_AT reached the pod path)", err)
	}
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %q (err=%v), want success", res.Outcome, res.Err)
	}

	stepControlsApplied.mu.Lock()
	got := stepControlsApplied.n
	stepControlsApplied.mu.Unlock()
	if got != 1 {
		t.Fatalf("step ran %d times, want 1 (an ambient SPARKWING_DRY_RUN turned the pod's work into an echo)", got)
	}
}
