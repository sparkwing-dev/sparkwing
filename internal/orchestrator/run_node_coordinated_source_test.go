package orchestrator_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type sourcedRunJob struct{ sparkwing.Base }

var sourcedRunRan struct {
	mu  sync.Mutex
	ran bool
}

func (sourcedRunJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(context.Context) error {
		sourcedRunRan.mu.Lock()
		defer sourcedRunRan.mu.Unlock()
		sourcedRunRan.ran = true
		return nil
	}), nil
}

type sourcedRunPipe struct{ sparkwing.Base }

func (sourcedRunPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "check", &sourcedRunJob{})
	return nil
}

var sourcedRunOnce sync.Once

func registerSourcedRunPipe() {
	sourcedRunOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("sourced-run",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return sourcedRunPipe{} })
	})
}

func TestRunNodeOnce_SourcedTriggerRouting(t *testing.T) {
	t.Run("coordinated local", func(t *testing.T) { testSourcedTriggerRouting(t, true) })
	t.Run("uncoordinated remote", func(t *testing.T) { testSourcedTriggerRouting(t, false) })
}

func testSourcedTriggerRouting(t *testing.T, coordinated bool) {
	t.Helper()
	sourcedRunRan.mu.Lock()
	sourcedRunRan.ran = false
	sourcedRunRan.mu.Unlock()
	registerSourcedRunPipe()
	isolateCheckout(t)
	isolateProfiles(t)
	t.Setenv("SPARKWING_GITCACHE_URL", "")
	t.Setenv("SPARKWING_LOCAL_ONLY", "1")

	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	if err := orchestrator.PathsAt(home).EnsureRoot(); err != nil {
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
		runID  = "run-sourced-trigger"
		nodeID = "check"
	)
	if err := st.CreateTriggerWithRun(ctx,
		store.Trigger{ID: runID, Pipeline: "sourced-run", RepoURL: "https://forge.example.test/acme/web.git"},
		store.Run{
			ID: runID, Pipeline: "sourced-run", Status: "running", StartedAt: time.Now(),
			RepoURL: "https://forge.example.test/acme/web.git",
		},
	); err != nil {
		t.Fatalf("create trigger with run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	var opts []orchestrator.RunNodeOption
	if coordinated {
		opts = append(opts, orchestrator.Coordinated())
	}
	res, err := orchestrator.RunNodeOnce(ctx, srv.URL, "", runID, nodeID,
		"node:"+runID+":"+nodeID, "", &captureLogger{}, quiet, nil, opts...)
	if !coordinated {
		if err == nil || !strings.Contains(err.Error(), "SPARKWING_GITCACHE_URL is unset") {
			t.Fatalf("remote routing error = %v", err)
		}
		sourcedRunRan.mu.Lock()
		defer sourcedRunRan.mu.Unlock()
		if sourcedRunRan.ran {
			t.Fatal("remote node ran the local pipeline")
		}
		return
	}
	if err != nil {
		t.Fatalf("RunNodeOnce: %v", err)
	}
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %q (err=%v), want success", res.Outcome, res.Err)
	}
	sourcedRunRan.mu.Lock()
	defer sourcedRunRan.mu.Unlock()
	if !sourcedRunRan.ran {
		t.Fatal("the registered pipeline's job never ran")
	}
}
