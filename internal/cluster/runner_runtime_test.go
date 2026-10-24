package cluster

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

type cliClaim func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)

func (f cliClaim) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return f(ctx, request)
}

type cliHost func(context.Context, controllerwire.ClaimedUnit) error

func (f cliHost) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	return f(ctx, unit)
}

type cliVersions struct {
	index engine.VerifiedIndex
	err   error
}

func (v cliVersions) Index(context.Context, time.Time) (engine.IndexResult, error) {
	return engine.IndexResult{Index: v.index}, v.err
}

func (v cliVersions) PrepareRunnerUpdate(context.Context, string, releaseasset.Target) (engine.SoftwareUpdate, error) {
	return engine.SoftwareUpdate{}, errors.New("unexpected update")
}

type cliWire struct {
	controllerwire.Controller
	claim cliClaim
}

func (w cliWire) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return w.claim(ctx, request)
}

func cliRuntime(claim cliClaim, host cliHost, versions cliVersions) runners.RuntimeConfig {
	return runners.RuntimeConfig{
		NewController: func(client.EngineOptions) (controllerwire.Controller, error) { return cliWire{claim: claim}, nil },
		Hosting:       host, Versions: versions, Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner},
	}
}

func cliAward(kind capability.UnitKind) controllerwire.ClaimedUnit {
	unit := controllerwire.ClaimedUnit{Unit: kind, UnitID: string(kind), RunID: "run", Capability: "private"}
	unit.Request = runspec.SealedRequest{RunID: "run", Request: runspec.Request{
		Git:    runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: strings.Repeat("a", 40)},
		Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/app.git", PipelineDir: ".sparkwing"}},
	}}
	return unit
}

func TestStartAndWarmExecuteTriggerAndJobOnOneManagedLoop(t *testing.T) {
	versions := cliVersions{index: cliVersionIndex(t)}
	for _, command := range []string{"start", "warm"} {
		t.Run(command, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			controller := "https://controller.example"
			credentials := runnerCredentials(t)
			concurrency := 1
			kinds := []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}
			var hosted []capability.UnitKind
			claims := 0
			cli := RunnerCLI{
				ReadConfig: func(string) (userconfig.Config, error) {
					return userconfig.Config{Runner: &userconfig.RunnerConfig{Controller: &controller, CredentialsDir: &credentials, MaxConcurrent: &concurrency, Repos: []string{"github.com/acme/*"}}}, nil
				},
				Factory: func(context.Context, RunnerOptions) (runners.RuntimeConfig, error) {
					return cliRuntime(func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
						if !reflect.DeepEqual(request.Kinds, []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode}) || request.RunnerVersion != "v1.2.3" {
							t.Errorf("claim: %#v", request)
						}
						unit := cliAward(kinds[claims])
						claims++
						return controllerwire.ClaimResponse{Claim: &unit}, nil
					}, func(ctx context.Context, unit controllerwire.ClaimedUnit) error {
						hosted = append(hosted, unit.Unit)
						if len(hosted) == 2 {
							cancel()
						}
						return nil
					}, versions), nil
				},
			}
			args := []string{"--config=/fixture/config.yaml"}
			if command == "warm" {
				args = []string{"--controller=" + controller, "--credentials-dir=" + credentials, "--max-concurrent=1", "--repos=github.com/acme/*"}
			}
			if err := cli.Run(ctx, command, args, "v1.2.3"); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(hosted, kinds) {
				t.Fatalf("hosted: %v", hosted)
			}
		})
	}
}

func TestRunnerRequiresSignedPolicyBeforeClaiming(t *testing.T) {
	failure := errors.New("signed policy unavailable")
	cli := RunnerCLI{Factory: func(context.Context, RunnerOptions) (runners.RuntimeConfig, error) {
		return cliRuntime(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			t.Error("claimed without signed policy")
			return controllerwire.ClaimResponse{}, nil
		}, func(context.Context, controllerwire.ClaimedUnit) error {
			t.Error("hosted without signed policy")
			return nil
		}, cliVersions{err: failure}), nil
	}}
	if err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", "--credentials-dir=" + runnerCredentials(t)}, "v1.2.3"); !errors.Is(err, failure) {
		t.Fatalf("Run = %v", err)
	}
}

func FuzzRegisteredRunnerNameCannotReplaceAwardIdentity(f *testing.F) {
	f.Add("desktop")
	f.Add("here:foreign-run")
	f.Add("pool=privileged")
	f.Fuzz(func(t *testing.T, name string) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		controller, credentials := "https://controller.example", runnerCredentials(t)
		concurrency := 1
		seen := false
		cli := RunnerCLI{
			ReadConfig: func(string) (userconfig.Config, error) {
				return userconfig.Config{Runner: &userconfig.RunnerConfig{Controller: &controller, CredentialsDir: &credentials, HolderPrefix: &name, MaxConcurrent: &concurrency, Repos: []string{"github.com/acme/*"}}}, nil
			},
			Factory: func(context.Context, RunnerOptions) (runners.RuntimeConfig, error) {
				return cliRuntime(func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					if request.RunID != "" || len(request.Labels) != 0 {
						t.Error("registration metadata changed claim selection")
					}
					unit := cliAward(capability.UnitNode)
					unit.Runner = nodeproto.RunnerIdentity{Name: "server-awarded", Type: "runner", Labels: []string{"linux"}}
					return controllerwire.ClaimResponse{Claim: &unit}, nil
				}, func(_ context.Context, unit controllerwire.ClaimedUnit) error {
					seen = true
					if unit.Runner.Name != "server-awarded" || !reflect.DeepEqual(unit.Runner.Labels, []string{"linux"}) {
						t.Error("registration metadata replaced awarded runner identity")
					}
					cancel()
					return nil
				}, cliVersions{index: cliVersionIndex(t)}), nil
			},
		}
		if err := cli.Run(ctx, "start", []string{"--config=/fixture/config.yaml"}, "v1.2.3"); !errors.Is(err, context.Canceled) || !seen {
			t.Fatalf("registered runner did not execute its award: seen=%v error=%v", seen, err)
		}
	})
}

func TestRunnerBindsMetricsBeforeClaiming(t *testing.T) {
	cli := RunnerCLI{Factory: func(context.Context, RunnerOptions) (runners.RuntimeConfig, error) {
		return cliRuntime(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			t.Error("claimed before metrics bound")
			return controllerwire.ClaimResponse{}, errors.New("unexpected claim")
		}, func(context.Context, controllerwire.ClaimedUnit) error { return nil }, cliVersions{index: cliVersionIndex(t)}), nil
	}}
	err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", "--credentials-dir=" + runnerCredentials(t), "--metrics-addr=invalid:address:port"}, "v1.2.3")
	if err == nil || !strings.Contains(err.Error(), "metrics") {
		t.Fatalf("metrics bind error: %v", err)
	}
}

func cliVersionIndex(t *testing.T) engine.VerifiedIndex {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	published := now.AddDate(0, -1, 0)
	policy := engine.VersionIndex{Format: engine.VersionIndexFormat, PublishedAt: now, Latest: "v1.2.3", Versions: []engineid.VersionInfo{{Version: "v1.2.3", PublishedAt: published, SupportUntil: published.AddDate(0, 12, 0)}}}
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	index, err := engine.VerifyIndex(engine.SignedIndex{Body: body, Signature: ed25519.Sign(private, body)}, now, []ed25519.PublicKey{public})
	if err != nil {
		t.Fatal(err)
	}
	return index
}
