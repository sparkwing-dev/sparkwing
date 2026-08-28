package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type podMaskInputs struct {
	Token string `flag:"token" desc:"deploy token" secret:"true"`
}

type podMaskPipe struct{ sparkwing.Base }

func (podMaskPipe) Plan(_ context.Context, plan *sparkwing.Plan, in podMaskInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "leak", func(ctx context.Context) error {
		sparkwing.Info(ctx, "deploying with token=%s now", in.Token)
		return nil
	})
	return nil
}

type podProgressPipe struct{ sparkwing.Base }

var podProgressContext chan context.Context

func (podProgressPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", func(ctx context.Context) error {
		podProgressContext <- ctx
		if _, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](ctx, "pod-progress-child", ""); err != nil {
			return err
		}
		if !orchestrator.ExpireProgressTimeoutForTest(ctx) {
			return errors.New("progress timeout did not resume after the child completed")
		}
		return ctx.Err()
	}).NoProgressTimeout(100 * time.Millisecond)
	return nil
}

func registerPodMaskPipe(t *testing.T) {
	t.Helper()
	if _, ok := sparkwing.Lookup("pod-mask-pipe"); ok {
		return
	}
	sparkwing.Register[podMaskInputs]("pod-mask-pipe",
		func() sparkwing.Pipeline[podMaskInputs] { return podMaskPipe{} })
}

func registerPodProgressPipe(t *testing.T) {
	t.Helper()
	if _, ok := sparkwing.Lookup("pod-progress-pipe"); ok {
		return
	}
	sparkwing.Register[sparkwing.NoInputs]("pod-progress-pipe",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return podProgressPipe{} })
}

func TestRunNodeOnce_NoProgressTimeoutPausesForChildAndResumesAfterward(t *testing.T) {
	registerPodProgressPipe(t)
	isolateProfiles(t)
	isolateCheckout(t)
	podProgressContext = make(chan context.Context, 1)

	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const (
		runID  = "run-pod-progress"
		nodeID = "parent"
	)
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "pod-progress-pipe", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	childFinished := make(chan error, 1)
	go func() {
		var progressCtx context.Context
		select {
		case progressCtx = <-podProgressContext:
		case <-ctx.Done():
			childFinished <- ctx.Err()
			return
		}
		poll := time.NewTicker(time.Millisecond)
		defer poll.Stop()
		var childID string
		for childID == "" || !orchestrator.ProgressTimeoutPausedForTest(progressCtx) {
			var findErr error
			childID, findErr = st.FindSpawnedChildTriggerID(ctx, runID, nodeID, "pod-progress-child")
			if findErr != nil {
				childFinished <- fmt.Errorf("find spawned child: %w", findErr)
				return
			}
			if childID != "" && orchestrator.ProgressTimeoutPausedForTest(progressCtx) {
				break
			}
			select {
			case <-poll.C:
			case <-ctx.Done():
				childFinished <- errors.New("spawned child was not recorded while progress timeout was paused")
				return
			}
		}
		if orchestrator.ExpireProgressTimeoutForTest(progressCtx) {
			childFinished <- errors.New("progress timeout fired while the delegated child was pending")
			return
		}
		finished := time.Now()
		if err := st.CreateRun(ctx, store.Run{
			ID: childID, Pipeline: "pod-progress-child", Status: "success", StartedAt: finished, FinishedAt: &finished,
		}); err != nil {
			childFinished <- fmt.Errorf("finish spawned child: %w", err)
			return
		}
		childFinished <- nil
	}()

	res, err := orchestrator.RunNodeOnce(ctx, srv.URL, "", runID, nodeID,
		"pod:"+runID+":"+nodeID, "", &captureLogger{}, quiet, nil)
	if err != nil {
		t.Fatalf("RunNodeOnce: %v", err)
	}
	select {
	case childErr := <-childFinished:
		if childErr != nil {
			t.Fatal(childErr)
		}
	case <-ctx.Done():
		t.Fatal("delegated child synchronization did not finish")
	}
	if res.Outcome != sparkwing.Failed {
		t.Fatalf("outcome = %q (err=%v), want no-progress failure after child completion", res.Outcome, res.Err)
	}
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].FailureReason != store.FailureNoProgressTimeout {
		t.Fatalf("nodes = %+v, want failure reason %q", nodes, store.FailureNoProgressTimeout)
	}
}

func TestRunNodeOnce_MasksSecretsInNodeLog(t *testing.T) {
	registerPodMaskPipe(t)
	isolateProfiles(t)
	isolateCheckout(t)

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
		secretValue = "pod-side-supersecret"
		runID       = "run-pod-mask"
		nodeID      = "leak"
	)
	if err := st.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "pod-mask-pipe",
		Status:    "running",
		StartedAt: time.Now(),
		Args:      map[string]string{"token": secretValue},
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	cap := &captureLogger{}
	res, err := orchestrator.RunNodeOnce(ctx, srv.URL, "", runID, nodeID,
		"pod:"+runID+":"+nodeID, "", cap, quiet, nil)
	if err != nil {
		t.Fatalf("RunNodeOnce: %v", err)
	}
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %q (err=%v), want success", res.Outcome, res.Err)
	}

	body, err := os.ReadFile(p.NodeLog(runID, nodeID))
	if err != nil {
		t.Fatalf("read node log: %v", err)
	}
	if strings.Contains(string(body), secretValue) {
		t.Fatalf("persisted node log leaks the raw secret value:\n%s", body)
	}
	if !strings.Contains(string(body), "token=*** now") {
		t.Fatalf("persisted node log missing the masked line:\n%s", body)
	}
	for _, rec := range cap.Snapshot() {
		if strings.Contains(rec.Msg, secretValue) {
			t.Fatalf("delegate received a record with the raw secret value: %+v", rec)
		}
	}
}
