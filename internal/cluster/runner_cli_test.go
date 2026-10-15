package cluster

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestStartUsesRunnerSectionAndReplacesRepos(t *testing.T) {
	controller, concurrent, heartbeat, lease := "https://controller.example", 2, "20s", "2m"
	boundary := errors.New("runtime boundary")
	cli := RunnerCLI{
		ReadConfig: func(path string) (userconfig.Config, error) {
			if path != "/fixture/config.yaml" {
				t.Errorf("config path %q", path)
			}
			return userconfig.Config{Runner: &userconfig.RunnerConfig{Controller: &controller, MaxConcurrent: &concurrent, Repos: []string{"github.com/old/*"}, Labels: []string{"linux"}, Heartbeat: &heartbeat, Lease: &lease}}, nil
		},
		Factory: func(_ context.Context, options RunnerOptions) (*runners.ManagedRunner, error) {
			if !reflect.DeepEqual(options.Config.Repos, []string{"github.com/a/*", "github.com/b/*", "github.com/c/*"}) {
				t.Errorf("repos: %v", options.Config.Repos)
			}
			if options.Loop.MaxConcurrent != 2 || options.Loop.Version != "v1.2.3" || !reflect.DeepEqual(options.Loop.Labels, []string{"linux"}) {
				t.Errorf("loop: %#v", options.Loop)
			}
			if options.Heartbeat != 20*time.Second || options.Lease != 2*time.Minute {
				t.Errorf("renewal: %v/%v", options.Heartbeat, options.Lease)
			}
			return nil, boundary
		},
	}
	err := cli.Run(t.Context(), "start", []string{"--config=/fixture/config.yaml", "--repos=github.com/a/*,github.com/b/*", "--repos=github.com/c/*"}, "v1.2.3")
	if !errors.Is(err, boundary) {
		t.Fatalf("Run = %v", err)
	}
}

func TestRunnerRejectsRemovedVerbsAndFlagsBeforeComposition(t *testing.T) {
	for _, command := range []string{"agent", "runner", "wingd"} {
		if err := (RunnerCLI{}).Run(t.Context(), command, nil, "v1.2.3"); err == nil {
			t.Errorf("accepted %s", command)
		}
	}
	for _, option := range []string{"--gitcache=http://cache", "--allow-repo=github.com/a/*", "--claim-nodes=false", "--trigger-sources=github", "--warm-modules=off", "--local-reserve=1", "--trigger-runner=warm", "--token=secret"} {
		if err := (RunnerCLI{}).Run(t.Context(), "warm", []string{option}, "v1.2.3"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("%s: %v", option, err)
		}
	}
}

func TestRunnerInvalidExplicitValuesNeverReachFactory(t *testing.T) {
	for _, option := range []string{"--max-concurrent=0", "--poll=0s", "--heartbeat=0s", "--lease=0s", "--heartbeat=3m", "--idle-exit=-1s", "--max-claims-before-restart=-1", "--repos=bad", "--warm-module=example.com/a@bad", "--keep-free=nonsense", "--cpu-ceiling=-1", "--memory-ceiling=9223372036854775808", "--memory-ceiling=0"} {
		t.Run(option, func(t *testing.T) {
			cli := RunnerCLI{Factory: func(context.Context, RunnerOptions) (*runners.ManagedRunner, error) {
				t.Error("invalid config reached factory")
				return nil, nil
			}}
			if err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", option}, "v1.2.3"); err == nil {
				t.Error("invalid flag accepted")
			}
		})
	}
}

func TestActionsExchangeUsesCallerContextAndServerPushBinding(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(t.Context(), contextKey{}, "caller")
	expires := time.Now().Add(time.Hour)
	boundary := errors.New("runtime boundary")
	push := capability.PushScope{RepositoryKey: "github.com/acme/project", Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), WorkflowRunID: "actions-run"}
	cli := RunnerCLI{
		Actions: func(got context.Context, controller, team string) (capability.Issued, error) {
			if got.Value(contextKey{}) != "caller" || controller != "https://controller.example" || team != "acme" {
				t.Error("Actions exchange lost caller context or selection")
			}
			return capability.Issued{Raw: "private", Capability: capability.Capability{Team: team, Binding: capability.Binding{Push: &push}, ExpiresAt: &expires}}, nil
		},
		Factory: func(got context.Context, options RunnerOptions) (*runners.ManagedRunner, error) {
			if got != ctx || options.Actions == nil || options.Actions.Capability.Binding.Push != &push {
				t.Error("runtime lost Actions scope")
			}
			if !options.Loop.ClaimUntil.Equal(expires) || !reflect.DeepEqual(options.Config.Repos, []string{push.RepositoryKey}) {
				t.Errorf("Actions loop: %#v", options)
			}
			return nil, boundary
		},
	}
	err := cli.Run(ctx, "warm", []string{"--controller=https://controller.example", "--github-actions", "--team=acme"}, "v1.2.3")
	if !errors.Is(err, boundary) {
		t.Fatalf("Run = %v", err)
	}
}

func TestRunnerUnwiredCompositionReturnsErrorWithoutCallingStubs(t *testing.T) {
	for _, command := range []string{"start", "warm"} {
		if err := (RunnerCLI{}).Run(t.Context(), command, []string{"--help"}, "v1.2.3"); err != nil {
			t.Fatal(err)
		}
		args := []string(nil)
		if command == "warm" {
			args = []string{"--controller=https://controller.example"}
		}
		if err := (RunnerCLI{}).Run(t.Context(), command, args, "v1.2.3"); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("%s: %v", command, err)
		}
	}
}

func TestWarmMapsPollingKindsAndClaimBudgetToUnifiedLoop(t *testing.T) {
	boundary := errors.New("runtime boundary")
	cli := RunnerCLI{Factory: func(_ context.Context, options RunnerOptions) (*runners.ManagedRunner, error) {
		if options.Loop.PollInterval != 2*time.Second || options.Loop.MaxClaims != 7 || !reflect.DeepEqual(options.Loop.Kinds, []capability.UnitKind{capability.UnitNode}) {
			t.Errorf("loop: %#v", options.Loop)
		}
		if options.CPUCeiling == nil || *options.CPUCeiling != .5 || options.MemoryCeiling == nil || *options.MemoryCeiling != 1<<30 {
			t.Errorf("resource ceilings lost")
		}
		return nil, boundary
	}}
	err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", "--poll=2s", "--max-claims-before-restart=7", "--also-claim-triggers=false", "--claim-jobs=true", "--cpu-ceiling=500m", "--memory-ceiling=1Gi"}, "v1.2.3")
	if !errors.Is(err, boundary) {
		t.Fatal(err)
	}
	cli.Factory = func(context.Context, RunnerOptions) (*runners.ManagedRunner, error) {
		t.Error("empty kinds reached factory")
		return nil, nil
	}
	if err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", "--also-claim-triggers=false", "--claim-jobs=false"}, "v1.2.3"); err == nil {
		t.Error("runner accepted no work kinds")
	}
}

func TestActionsCancellationAndInvalidScopePreventRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cli := RunnerCLI{Actions: func(ctx context.Context, _, _ string) (capability.Issued, error) {
		return capability.Issued{}, context.Cause(ctx)
	}, Factory: func(context.Context, RunnerOptions) (*runners.ManagedRunner, error) {
		t.Error("invalid exchange reached runtime")
		return nil, nil
	}}
	args := []string{"--controller=https://controller.example", "--github-actions", "--team=team"}
	if err := cli.Run(ctx, "warm", args, "v1.2.3"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	cli.Actions = func(context.Context, string, string) (capability.Issued, error) {
		return capability.Issued{Raw: "private"}, nil
	}
	if err := cli.Run(t.Context(), "warm", args, "v1.2.3"); err == nil {
		t.Error("unbound Actions capability accepted")
	}
}

func TestRunnerRejectsSelectorsMissingFromControllerContract(t *testing.T) {
	cli := RunnerCLI{Factory: func(context.Context, RunnerOptions) (*runners.ManagedRunner, error) {
		t.Error("unsupported selector reached runtime")
		return nil, nil
	}}
	for _, option := range []string{"--trigger-source=github", "--holder-prefix=worker"} {
		if err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", option}, "v1.2.3"); err == nil || !strings.Contains(err.Error(), "claim contract") {
			t.Fatalf("%s: %v", option, err)
		}
	}
}
