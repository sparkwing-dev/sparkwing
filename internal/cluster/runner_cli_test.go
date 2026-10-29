package cluster

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestStartUsesRunnerSectionAndReplacesRepos(t *testing.T) {
	box := "configured-box"
	controller, concurrent, heartbeat, lease := "https://controller.example", 2, "20s", "2m"
	credentials := runnerCredentials(t)
	boundary := errors.New("runtime boundary")
	cli := RunnerCLI{
		ReadConfig: func(path string) (userconfig.Config, error) {
			if path != "/fixture/config.yaml" {
				t.Errorf("config path %q", path)
			}
			return userconfig.Config{Machine: &userconfig.MachineConfig{BoxID: &box}, Runner: &userconfig.RunnerConfig{Controller: &controller, CredentialsDir: &credentials, MaxConcurrent: &concurrent, Repos: []string{"github.com/old/*"}, Labels: []string{"linux"}, Heartbeat: &heartbeat, Lease: &lease}}, nil
		},
		Factory: func(_ context.Context, options RunnerOptions) (runners.RuntimeConfig, error) {
			if options.BoxID == nil || *options.BoxID != box {
				t.Fatalf("lost configured box identity: %+v", options.BoxID)
			}
			if !reflect.DeepEqual(options.Config.Repos, []string{"github.com/a/*", "github.com/b/*", "github.com/c/*"}) {
				t.Errorf("repos: %v", options.Config.Repos)
			}
			if options.Loop.MaxConcurrent != 2 || options.Loop.Version != "v1.2.3" || !reflect.DeepEqual(options.Loop.Labels, []string{"linux"}) {
				t.Errorf("loop: %#v", options.Loop)
			}
			if options.Heartbeat != 20*time.Second || options.Lease != 2*time.Minute {
				t.Errorf("renewal: %v/%v", options.Heartbeat, options.Lease)
			}
			return runners.RuntimeConfig{}, boundary
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
	for _, option := range []string{"--max-concurrent=0", "--poll=0s", "--heartbeat=0s", "--lease=0s", "--heartbeat=3m", "--idle-exit=-1s", "--max-claims-before-restart=-1", "--repos=bad", "--warm-module=example.com/a@bad", "--keep-free=nonsense", "--submit-env-allow=*", "--cpu-ceiling=-1", "--memory-ceiling=9223372036854775808", "--memory-ceiling=0"} {
		t.Run(option, func(t *testing.T) {
			cli := RunnerCLI{Factory: func(context.Context, RunnerOptions) (runners.RuntimeConfig, error) {
				t.Error("invalid config reached factory")
				return runners.RuntimeConfig{}, nil
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
	push := capability.PushScope{RepositoryKey: "github.com/acme/project", Repository: "acme/repo", Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), WorkflowRunID: "actions-run", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main"}
	cli := RunnerCLI{
		Actions: func(got context.Context, controller, team string) (capability.Issued, error) {
			if got.Value(contextKey{}) != "caller" || controller != "https://controller.example" || team != "acme" {
				t.Error("Actions exchange lost caller context or selection")
			}
			return capability.Issued{Raw: "private", Capability: capability.Capability{Team: team, Binding: capability.Binding{Push: &push}, ExpiresAt: &expires}}, nil
		},
		Factory: func(got context.Context, options RunnerOptions) (runners.RuntimeConfig, error) {
			if got != ctx || options.Bearer == nil {
				t.Error("runtime lost Actions scope")
			}
			if raw, err := options.Bearer(got); err != nil || raw != "private" {
				t.Fatalf("runtime lost Actions credential: %v", err)
			}
			if !options.Loop.ClaimUntil.Equal(expires) || !reflect.DeepEqual(options.Config.Repos, []string{"github.com/" + push.Repository}) {
				t.Errorf("Actions loop: %#v", options)
			}
			return runners.RuntimeConfig{}, boundary
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
		cli := RunnerCLI{ReadConfig: func(string) (userconfig.Config, error) {
			controller := "https://controller.example"
			return userconfig.Config{Runner: &userconfig.RunnerConfig{Controller: &controller}}, nil
		}}
		if command == "warm" {
			args = []string{"--controller=https://controller.example"}
		}
		if err := cli.Run(t.Context(), command, args, "v1.2.3"); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("%s: %v", command, err)
		}
	}
}

func TestWarmMapsPollingKindsAndClaimBudgetToUnifiedLoop(t *testing.T) {
	boundary := errors.New("runtime boundary")
	cli := RunnerCLI{Factory: func(_ context.Context, options RunnerOptions) (runners.RuntimeConfig, error) {
		if options.Loop.PollInterval != 2*time.Second || options.Loop.MaxClaims != 7 || !reflect.DeepEqual(options.Loop.Kinds, []capability.UnitKind{capability.UnitEval, capability.UnitNode}) {
			t.Errorf("loop: %#v", options.Loop)
		}
		return runners.RuntimeConfig{}, boundary
	}}
	err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", "--credentials-dir=" + runnerCredentials(t), "--poll=2s", "--max-claims-before-restart=7", "--also-claim-triggers=false", "--claim-jobs=true"}, "v1.2.3")
	if !errors.Is(err, boundary) {
		t.Fatal(err)
	}
	cli.Factory = func(context.Context, RunnerOptions) (runners.RuntimeConfig, error) {
		t.Error("empty kinds reached factory")
		return runners.RuntimeConfig{}, nil
	}
	if err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", "--also-claim-triggers=false", "--claim-jobs=false"}, "v1.2.3"); err == nil {
		t.Error("runner accepted no work kinds")
	}
}

func runnerCredentials(t *testing.T) string {
	t.Helper()
	directory, err := fssecure.MkdirPrivateTemp(t.TempDir(), "credentials-")
	if err != nil {
		t.Fatal(err)
	}
	if err := fssecure.WriteFile(filepath.Join(directory, "agent-token"), []byte("private-runner-credential")); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestActionsCancellationAndInvalidScopePreventRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cli := RunnerCLI{Actions: func(ctx context.Context, _, _ string) (capability.Issued, error) {
		return capability.Issued{}, context.Cause(ctx)
	}, Factory: func(context.Context, RunnerOptions) (runners.RuntimeConfig, error) {
		t.Error("invalid exchange reached runtime")
		return runners.RuntimeConfig{}, nil
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
	cli := RunnerCLI{Factory: func(context.Context, RunnerOptions) (runners.RuntimeConfig, error) {
		t.Error("unsupported selector reached runtime")
		return runners.RuntimeConfig{}, nil
	}}
	for _, option := range []string{"--trigger-source=github", "--holder-prefix=worker"} {
		if err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", option}, "v1.2.3"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("%s: %v", option, err)
		}
	}
}

func TestWarmCarriesTheOperatorSubmitAllowList(t *testing.T) {
	boundary := errors.New("runtime boundary")
	credentials := runnerCredentials(t)
	cli := RunnerCLI{Factory: func(_ context.Context, options RunnerOptions) (runners.RuntimeConfig, error) {
		base, err := procenv.WithSubmitted(procenv.Base{Masker: secrets.NewMasker()}, map[string][]byte{"BUILD_REGION": []byte("eu"), "DEPLOY_TARGET": []byte("prod")}, options.SubmitAccept)
		if err != nil || !reflect.DeepEqual(base.Ambient, []string{"BUILD_REGION=eu"}) {
			t.Errorf("operator allow-list admitted %v: %v", base.Ambient, err)
		}
		return runners.RuntimeConfig{}, boundary
	}}
	err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", "--credentials-dir=" + credentials, "--submit-env-allow=BUILD_*"}, "v1.2.3")
	if !errors.Is(err, boundary) {
		t.Fatalf("Run = %v", err)
	}
}

func TestWarmRejectsInertFlags(t *testing.T) {
	for _, option := range []string{"--cpu-ceiling=1", "--memory-ceiling=1Gi", "--deadline=1h", "--warm-module=off", "--accept-working-tree", "--holder-prefix=worker", "--trigger-source=github", "--heartbeat=10s", "--lease=1m"} {
		t.Run(option, func(t *testing.T) {
			err := (RunnerCLI{}).Run(t.Context(), "warm", []string{"--controller=https://controller.example", option}, "v1.2.3")
			if err == nil || !strings.Contains(err.Error(), "unknown flag") {
				t.Fatalf("expected unknown flag, got %v", err)
			}
		})
	}
}
