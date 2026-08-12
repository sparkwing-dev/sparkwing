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
			Args:       map[string]string{"replicas": "7"},
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
			Args:       map[string]string{"replicas": "7"},
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

// A guard's whole job is to describe an invocation nobody may
// dispatch. Where the offending value came from is not part of that
// description: `arg:protected=prod` must fire on the pipeline entry's
// own args: block exactly as it fires on `--protected prod`, because
// the run reaches prod either way.
func TestRun_ArgGuardSeesPipelineYAMLDefault(t *testing.T) {
	registerLockDefaultsPipe(t)
	p := newPaths(t)
	_, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "lock-defaults-pipe",
		PipelineYAML: &pipelines.Pipeline{
			Name:       "lock-defaults-pipe",
			Entrypoint: "LockDefaults",
			Args:       map[string]string{"protected": "prod"},
			Guards: pipelines.Guards{
				Reject: []string{"arg:protected=prod"},
			},
		},
	})
	if err == nil {
		t.Fatal("guard did not fire on a yaml-supplied value; the run dispatched against prod")
	}
	if !strings.Contains(err.Error(), "arg:protected=prod") {
		t.Errorf("error = %v, want the rejecting guard token named", err)
	}
}

// The project's defaults.args block is the lowest layer, and a guard
// has to see through to it for the same reason.
func TestRun_ArgGuardSeesProjectDefaultArgs(t *testing.T) {
	registerLockDefaultsPipe(t)
	p := newPaths(t)
	_, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:    "lock-defaults-pipe",
		DefaultArgs: map[string]string{"protected": "prod"},
		PipelineYAML: &pipelines.Pipeline{
			Name:       "lock-defaults-pipe",
			Entrypoint: "LockDefaults",
			Guards: pipelines.Guards{
				Reject: []string{"arg:protected=prod"},
			},
		},
	})
	if err == nil {
		t.Fatal("guard did not fire on a defaults.args value; the run dispatched against prod")
	}
}

// Guards read the merged set, which means they read the CLI override
// too: a flag that steers the run away from prod must not be judged
// against the yaml value it replaced.
func TestRun_ArgGuardHonoursCLIOverrideOfYAMLDefault(t *testing.T) {
	registerLockDefaultsPipe(t)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "lock-defaults-pipe",
		Args:     map[string]string{"protected": "staging"},
		PipelineYAML: &pipelines.Pipeline{
			Name:       "lock-defaults-pipe",
			Entrypoint: "LockDefaults",
			Args:       map[string]string{"protected": "prod"},
			Guards: pipelines.Guards{
				Reject: []string{"arg:protected=prod"},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); the override took the run off prod, so the guard must not fire",
			res.Status, res.Error)
	}
}

// An arg no layer supplies stays unset, and an arg guard never
// matches an unset arg. Unchanged by the merge.
func TestRun_ArgGuardOnUnsetArgDoesNotFire(t *testing.T) {
	registerLockDefaultsPipe(t)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "lock-defaults-pipe",
		PipelineYAML: &pipelines.Pipeline{
			Name:       "lock-defaults-pipe",
			Entrypoint: "LockDefaults",
			Guards: pipelines.Guards{
				Reject: []string{"arg:protected=prod"},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); an unset arg matches no arg guard", res.Status, res.Error)
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
				Reject: []string{"profile:local"},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("absent profile shouldn't trip profile:local guard; got %q (err=%v)", res.Status, res.Error)
	}
}
