package orchestrator_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// --- Verify: success path ---

type verifyOKPipe struct{ sparkwing.Base }

var verifyOKRan atomic.Bool

func (verifyOKPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error { return nil }).
		Verify(func(ctx context.Context) error {
			verifyOKRan.Store(true)
			return nil
		})
	return nil
}

// --- Verify: failure routes to OnFailure with StageVerify ---

type verifyFailsPipe struct{ sparkwing.Base }

var (
	vfActionRuns    atomic.Int32
	vfVerifyRuns    atomic.Int32
	vfRecoveryStage atomic.Int32 // int(stage)+1; 0 = recovery never ran
	vfRecoveryErr   atomic.Value // string
)

func (verifyFailsPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		vfActionRuns.Add(1)
		return nil
	}).
		Verify(func(ctx context.Context) error {
			vfVerifyRuns.Add(1)
			return errors.New("unhealthy")
		}).
		OnFailure("recover", func(ctx context.Context, f sparkwing.Failure) error {
			vfRecoveryStage.Store(int32(f.Stage) + 1)
			if f.Err != nil {
				vfRecoveryErr.Store(f.Err.Error())
			}
			return nil
		})
	return nil
}

// --- Verify: action failure never runs verify, routes with StageAction ---

type verifyActionFailsPipe struct{ sparkwing.Base }

var (
	afVerifyRuns    atomic.Int32
	afRecoveryStage atomic.Int32
)

func (verifyActionFailsPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		return errors.New("deploy failed")
	}).
		Verify(func(ctx context.Context) error {
			afVerifyRuns.Add(1)
			return nil
		}).
		OnFailure("recover", func(ctx context.Context, f sparkwing.Failure) error {
			afRecoveryStage.Store(int32(f.Stage) + 1)
			return nil
		})
	return nil
}

// --- Verify: Retry re-runs action + verify together ---

type verifyRetryPipe struct{ sparkwing.Base }

var (
	vrActionRuns atomic.Int32
	vrVerifyRuns atomic.Int32
)

func (verifyRetryPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		vrActionRuns.Add(1)
		return nil
	}).
		Verify(func(ctx context.Context) error {
			if vrVerifyRuns.Add(1) <= 2 {
				return errors.New("not healthy yet")
			}
			return nil
		}).
		Retry(3)
	return nil
}

func init() {
	register("verify-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &verifyOKPipe{} })
	register("verify-fails", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &verifyFailsPipe{} })
	register("verify-action-fails", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &verifyActionFailsPipe{} })
	register("verify-retry", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &verifyRetryPipe{} })
}

func nodesByID(t *testing.T, p orchestrator.Paths, runID string) map[string]*store.Node {
	t.Helper()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	nodes, err := st.ListNodes(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	byID := make(map[string]*store.Node, len(nodes))
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	return byID
}

func TestVerify_SuccessLetsNodeSucceed(t *testing.T) {
	verifyOKRan.Store(false)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "verify-ok"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success", res.Status)
	}
	if !verifyOKRan.Load() {
		t.Fatal("verify check never ran")
	}
}

func TestVerify_FailureFailsNodeAtVerifyStage(t *testing.T) {
	vfActionRuns.Store(0)
	vfVerifyRuns.Store(0)
	vfRecoveryStage.Store(0)
	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "verify-fails"})

	if res.Status != "failed" {
		t.Fatalf("run status = %q, want failed (verify failed)", res.Status)
	}
	if got := vfActionRuns.Load(); got != 1 {
		t.Fatalf("action ran %d times, want 1", got)
	}
	if got := vfVerifyRuns.Load(); got != 1 {
		t.Fatalf("verify ran %d times, want 1", got)
	}

	byID := nodesByID(t, p, res.RunID)
	if byID["deploy"].Outcome != string(sparkwing.Failed) {
		t.Fatalf("deploy outcome = %q, want failed", byID["deploy"].Outcome)
	}
	if byID["deploy"].FailureReason != store.FailureVerify {
		t.Fatalf("deploy failure reason = %q, want %q", byID["deploy"].FailureReason, store.FailureVerify)
	}
	if byID["recover"].Outcome != string(sparkwing.Success) {
		t.Fatalf("recover outcome = %q, want success", byID["recover"].Outcome)
	}

	// Recovery saw StageVerify, with the check's original (unwrapped) error.
	if got := vfRecoveryStage.Load(); got != int32(sparkwing.StageVerify)+1 {
		t.Fatalf("recovery stage = %d, want StageVerify", got-1)
	}
	if msg, _ := vfRecoveryErr.Load().(string); msg != "unhealthy" {
		t.Fatalf("recovery saw err %q, want %q (unwrapped)", msg, "unhealthy")
	}
}

func TestVerify_ActionFailureSkipsVerifyAndRoutesAsAction(t *testing.T) {
	afVerifyRuns.Store(0)
	afRecoveryStage.Store(0)
	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "verify-action-fails"})

	if res.Status != "failed" {
		t.Fatalf("run status = %q, want failed", res.Status)
	}
	if got := afVerifyRuns.Load(); got != 0 {
		t.Fatalf("verify ran %d times on an action failure, want 0", got)
	}
	if got := afRecoveryStage.Load(); got != int32(sparkwing.StageAction)+1 {
		t.Fatalf("recovery stage = %d, want StageAction", got-1)
	}
}

func TestVerify_RetryReRunsActionAndVerify(t *testing.T) {
	vrActionRuns.Store(0)
	vrVerifyRuns.Store(0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "verify-retry"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success (verify passes on 3rd attempt)", res.Status)
	}
	if got := vrActionRuns.Load(); got != 3 {
		t.Fatalf("action ran %d times, want 3", got)
	}
	if got := vrVerifyRuns.Load(); got != 3 {
		t.Fatalf("verify ran %d times, want 3", got)
	}
}
