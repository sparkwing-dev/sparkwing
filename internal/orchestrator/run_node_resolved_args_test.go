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

type podArgsArgs struct {
	Replicas int `desc:"replica count"`
}

type podArgsJob struct {
	sparkwing.Base
	sparkwing.WithArgs[podArgsArgs]
}

func (podArgsJob) Schema() (*sparkwing.Schema, error) {
	s := sparkwing.NewSchema[podArgsArgs]()
	s.Field("Replicas").Default(1)
	return s.Build()
}

var podArgsSeen struct {
	mu sync.Mutex
	n  int
}

func (j *podArgsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) error {
		podArgsSeen.mu.Lock()
		defer podArgsSeen.mu.Unlock()
		podArgsSeen.n = sparkwing.ArgOrDefault(ctx, "replicas", -1)
		return nil
	}), nil
}

type podArgsPipe struct{ sparkwing.Base }

func (podArgsPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "scale", &podArgsJob{})
	return nil
}

var podArgsOnce sync.Once

func registerPodArgsPipe() {
	podArgsOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("pod-resolved-args",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return podArgsPipe{} })
	})
}

func TestRunNodeOnce_InstallsResolvedArgs(t *testing.T) {
	registerPodArgsPipe()
	isolateCheckout(t)
	isolateProfiles(t)

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
		runID  = "run-resolved-args"
		nodeID = "scale"
	)
	if err := st.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "pod-resolved-args",
		Status:    "running",
		StartedAt: time.Now(),
		Args:      map[string]string{"replicas": "5"},
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	podArgsSeen.mu.Lock()
	podArgsSeen.n = 0
	podArgsSeen.mu.Unlock()

	res, err := orchestrator.RunNodeOnce(ctx, srv.URL, "", runID, nodeID,
		"pod:"+runID+":"+nodeID, "", &captureLogger{}, quiet, nil)
	if err != nil {
		t.Fatalf("RunNodeOnce: %v", err)
	}
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %q (err=%v), want success", res.Outcome, res.Err)
	}

	podArgsSeen.mu.Lock()
	got := podArgsSeen.n
	podArgsSeen.mu.Unlock()
	if got != 5 {
		t.Fatalf("ArgOrDefault(replicas) = %d, want 5 (1 means the resolved args never reached the node)", got)
	}
}
