package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var lockDefaultsCapturedReplicas int

type lockDefaultsInputs struct {
	Replicas  int    `flag:"replicas" desc:"replica count"`
	Protected string `flag:"protected" desc:"approval mode"`
}

type lockDefaultsPipe struct{ sparkwing.Base }

func (lockDefaultsPipe) Plan(_ context.Context, plan *sparkwing.Plan, in lockDefaultsInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "rollout", func(_ context.Context) error {
		lockDefaultsCapturedReplicas = in.Replicas
		return nil
	})
	return nil
}

func registerLockDefaultsPipe(t *testing.T) {
	t.Helper()
	if _, ok := sparkwing.Lookup("lock-defaults-pipe"); ok {
		return
	}
	sparkwing.Register[lockDefaultsInputs]("lock-defaults-pipe", func() sparkwing.Pipeline[lockDefaultsInputs] {
		return lockDefaultsPipe{}
	})
}

func TestRun_PipelineDefaultsFillUnsetArgs(t *testing.T) {
	registerLockDefaultsPipe(t)
	lockDefaultsCapturedReplicas = 0
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "lock-defaults-pipe",
		PipelineYAML: &pipelines.Pipeline{
			Name:       "lock-defaults-pipe",
			Entrypoint: "LockDefaults",
			Defaults:   map[string]string{"replicas": "7"},
		},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}
	if lockDefaultsCapturedReplicas != 7 {
		t.Errorf("YAML default should fill --replicas; got %d, want 7", lockDefaultsCapturedReplicas)
	}
}

func TestRun_ExplicitArgBeatsPipelineDefault(t *testing.T) {
	registerLockDefaultsPipe(t)
	lockDefaultsCapturedReplicas = 0
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "lock-defaults-pipe",
		Args:     map[string]string{"replicas": "3"},
		PipelineYAML: &pipelines.Pipeline{
			Name:       "lock-defaults-pipe",
			Entrypoint: "LockDefaults",
			Defaults:   map[string]string{"replicas": "7"},
		},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}
	if lockDefaultsCapturedReplicas != 3 {
		t.Errorf("explicit --replicas should win; got %d, want 3", lockDefaultsCapturedReplicas)
	}
}

func TestRun_LockedFlagRejected(t *testing.T) {
	registerLockDefaultsPipe(t)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "lock-defaults-pipe",
		Args:     map[string]string{"protected": "true"},
		PipelineYAML: &pipelines.Pipeline{
			Name:       "lock-defaults-pipe",
			Entrypoint: "LockDefaults",
			Locked:     []string{"protected"},
		},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed (locked flag should reject)", res.Status)
	}
	if res.Error == nil || !strings.Contains(res.Error.Error(), "locked by pipeline") {
		t.Errorf("error should name the lock; got %v", res.Error)
	}
}

func TestRun_GuardRejectFiresBeforeDispatch(t *testing.T) {
	registerLockDefaultsPipe(t)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "lock-defaults-pipe",
		PipelineYAML: &pipelines.Pipeline{
			Name:       "lock-defaults-pipe",
			Entrypoint: "LockDefaults",
			Guards: pipelines.Guards{
				Reject: []string{"profile-local"},
			},
		},
	})
	// With no opts.Profile set, ProfileIsLocal is false so the guard
	// doesn't fire -- the run succeeds.
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("absent profile shouldn't trip profile-local guard; got %q (err=%v)", res.Status, res.Error)
	}
}
