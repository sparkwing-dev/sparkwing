package acceptance_test

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runnerIdentityFixtureW8() (controllerwire.ClaimedUnit, nodehost.NodeInput) {
	unit := runnerAuthorityAwardW8(capability.UnitNode)
	return unit, runnerIdentityInputW8(unit)
}

func bindRunnerIdentityW8(unit *controllerwire.ClaimedUnit) {
	identity := engine.Identity{Pin: engine.Pin{SDK: engine.GoSDK, Version: "v0.20.0", Manifest: "go.mod", InputsDigest: strings.Repeat("a", 64)}, Version: "v0.20.0", ManifestDigest: strings.Repeat("b", 64)}
	unit.Engine = &identity
	unit.Request = runspec.SealedRequest{RunID: unit.RunID, Hash: "request-hash", Request: runspec.Request{Team: "team"}}
	unit.Accepted = &controllerwire.PlanAccepted{Spec: runspec.Spec{Request: unit.Request, Hash: "spec-hash", Resolved: runspec.Resolved{Engine: identity}}, Plan: []byte(`{"jobs":[{"id":"job"}]}`), PlanHash: "plan-hash"}
	unit.Execution = &runspec.JobExecutionSettings{}
	unit.Runner = nodeproto.RunnerIdentity{Name: "granted-runner", Type: "runner", Labels: []string{"pool=granted", "linux"}}
}

func runnerIdentityInputW8(unit controllerwire.ClaimedUnit) nodehost.NodeInput {
	entry := &source.Entry{Engine: engine.Resolved{Identity: *unit.Engine, Executable: "/private/verified-engine", AssetDigest: strings.Repeat("c", 64), Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}}
	return nodehost.NodeInput{Entry: entry}
}

type identityNodeW8 func(nodehost.NodeInput)

func (host identityNodeW8) Execute(_ context.Context, input nodehost.NodeInput) error {
	host(input)
	return nil
}

func runIdentityW8(t *testing.T, unit controllerwire.ClaimedUnit, input nodehost.NodeInput, execute func(nodehost.NodeInput)) error {
	t.Helper()
	prepared, joined, cleaned := false, false, false
	host := runners.Hosting{
		ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
		NewController: runnerControllerFactoryW8(func(context.Context) (lease.Beat, error) { return lease.Beat{}, nil }),
		StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) {
			return ctx, func() { joined = true }
		},
		PrepareNode: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
			prepared = true
			return identityNodeW8(execute), input, func() error {
				if !joined {
					t.Error("source closed before keeper joined")
				}
				cleaned = true
				return nil
			}, nil
		},
	}
	err := host.Host(t.Context(), unit)
	if prepared && (!joined || !cleaned) {
		t.Fatal("execution identity check bypassed keeper or source cleanup")
	}
	return err
}

func TestRunnerExecutionIdentityW8Components(t *testing.T) {
	t.Setenv("SPARKWING_RUNNER_NAME", "forged-ambient-runner")
	t.Setenv("SPARKWING_RUNNER_TYPE", "cloud")
	t.Setenv("SPARKWING_RUNNER_LABELS", "pool=forged")
	unit, input := runnerIdentityFixtureW8()
	input.Engine = engine.Resolved{Identity: engine.Identity{Version: "v9.9.9"}, Executable: "/ambient/engine"}
	input.GrantedRunner = nodeproto.RunnerIdentity{Name: "ambient-runner", Type: "cloud", Labels: []string{"pool=forged"}}
	input.NodeID, input.Generation, input.Attempt = "other-job", 999, 999
	input.Accepted = nodehost.AcceptedRun{RunID: "other-run", SpecHash: "other-spec"}
	input.Execution.Retry.Value = 999
	input.RouteBinding = nodeproto.ProcessBinding{RunID: unit.RunID, JobID: unit.JobID, Routes: nodeproto.ProcessRoutes{URL: "http://private-host", Bearer: "delegated-route"}}
	input.SpecHash = "persisted-job-hash"
	called := false
	if err := runIdentityW8(t, unit, input, func(got nodehost.NodeInput) {
		called = true
		if !reflect.DeepEqual(got.Engine, input.Entry.Engine) || !reflect.DeepEqual(got.GrantedRunner, unit.Runner) || got.NodeID != unit.JobID || got.Generation != unit.Binding.Generation || got.Attempt != unit.Binding.Attempt {
			t.Error("execution did not receive the granted runner, selected engine and claim fence")
		}
		if got.SpecHash != input.SpecHash || got.Entry != input.Entry || got.RouteBinding != input.RouteBinding {
			t.Error("identity binding changed host-owned preparation")
		}
		if got.Accepted.RunID != unit.RunID || got.Accepted.SpecHash != string(unit.Accepted.Spec.Hash) || got.Accepted.PlanHash != string(unit.Accepted.PlanHash) || !reflect.DeepEqual(got.Execution, *unit.Execution) {
			t.Error("preparation replaced the awarded seals or job settings")
		}
		got.GrantedRunner.Labels[0] = "mutated-by-host"
	}); err != nil || !called {
		t.Fatalf("execution identity: called=%v error=%v", called, err)
	}
	if unit.Runner.Labels[0] != "pool=granted" {
		t.Fatal("execution mutated the server award's identity")
	}
}

func TestRunnerExecutionIdentityRefusesUnboundSourceW8(t *testing.T) {
	for _, field := range []string{"missing_engine", "missing_accepted", "accepted_engine", "request_run", "accepted_run", "team", "runner_name", "runner_type", "missing_entry", "source_version", "source_pin", "source_manifest", "missing_executable", "wrong_binary"} {
		t.Run(field, func(t *testing.T) {
			unit, input := runnerIdentityFixtureW8()
			switch field {
			case "missing_engine":
				unit.Engine = nil
			case "missing_accepted":
				unit.Accepted = nil
			case "accepted_engine":
				unit.Accepted.Spec.Resolved.Engine.Version = "v9.9.9"
			case "request_run":
				unit.Request.RunID = "foreign"
			case "accepted_run":
				unit.Accepted.Spec.Request.RunID = "foreign"
			case "team":
				unit.Accepted.Spec.Request.Request.Team = "foreign"
			case "runner_name":
				unit.Runner.Name = ""
			case "runner_type":
				unit.Runner.Type = ""
			case "missing_entry":
				input.Entry = nil
			case "source_version":
				input.Entry.Engine.Identity.Version = "v9.9.9"
			case "source_pin":
				input.Entry.Engine.Identity.Pin.InputsDigest = strings.Repeat("d", 64)
			case "source_manifest":
				input.Entry.Engine.Identity.ManifestDigest = strings.Repeat("e", 64)
			case "missing_executable":
				input.Entry.Engine.Executable = ""
			case "wrong_binary":
				input.Entry.Engine.Target.Binary = releaseasset.SparkwingRunner
			}
			if err := runIdentityW8(t, unit, input, func(nodehost.NodeInput) { t.Error("unbound source executed") }); err == nil {
				t.Fatal("unbound execution identity accepted")
			}
		})
	}
}

func TestCloudWorkUsesRunnerExecutionIdentityW8(t *testing.T) {
	_, cfg, award := cloudFixture(launcher.CloudWork)
	unit, input := runnerIdentityFixtureW8()
	binding := award.Binding
	unit.UnitID, unit.RunID, unit.JobID = binding.UnitID, binding.RunID, binding.NodeID
	unit.Binding = capability.Binding{Kind: capability.BindingNode, RunID: binding.RunID, NodeID: binding.NodeID, Holder: binding.Holder, Generation: binding.Generation, Attempt: binding.Attempt}
	unit.Engine = award.Engine
	unit.Accepted.Spec.Resolved.Engine, input.Entry.Engine.Identity = *award.Engine, *award.Engine
	unit.OwnerDeadline, unit.AttemptDeadline, unit.ParentCapabilityExpiry = &binding.OwnerDeadline, &binding.AttemptDeadline, &binding.ParentCapabilityExpiry
	unit.Execution = &runspec.JobExecutionSettings{Resources: runspec.Setting[runspec.Resources]{Optional: runspec.Optional[runspec.Resources]{Set: true, Value: runspec.Resources{
		Cores:       runspec.Setting[float64]{Optional: runspec.Optional[float64]{Set: true, Value: award.CPUCores}},
		MemoryBytes: runspec.Setting[int64]{Optional: runspec.Optional[int64]{Set: true, Value: award.Memory}},
	}}}}
	unit.Request.Request.Git = runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: strings.Repeat("a", 40)}
	unit.Request.Request.Source.Location = runspec.SourceLocation{RemoteURL: "https://github.com/team/repo.git", PipelineDir: ".sparkwing"}
	unit.Accepted.Spec.Request = unit.Request
	var err error
	award.Source, err = source.ForExecution(unit.Accepted.Spec)
	if err != nil {
		t.Fatal(err)
	}
	input.GrantedRunner = nodeproto.RunnerIdentity{Name: "runner-image-identity"}
	input.Engine = engine.Resolved{Executable: "/runner-image/engine"}
	exchanges, executions := 0, 0
	host := launcher.BootstrapHost{
		ControllerURL: cfg.ControllerURL,
		NewController: func(client.EngineOptions) (controllerwire.Controller, error) {
			return bootstrapExchangeW8{exchange: func(context.Context) (controllerwire.ExchangeResponse, error) {
				exchanges++
				return controllerwire.ExchangeResponse{Claim: unit}, nil
			}}, nil
		},
		Host: bootstrapHostW8(func(_ context.Context, got controllerwire.ClaimedUnit) error {
			return runIdentityW8(t, got, input, func(execution nodehost.NodeInput) {
				executions++
				if !reflect.DeepEqual(execution.GrantedRunner, unit.Runner) || execution.Engine.Identity != *award.Engine || execution.Engine.Executable != input.Entry.Engine.Executable {
					t.Error("Cloud work used the runner image's identity or engine")
				}
			})
		}),
	}
	if err := host.Run(t.Context(), award); err != nil || exchanges != 1 || executions != 1 {
		t.Fatalf("Cloud work identity: exchanges=%d executions=%d error=%v", exchanges, executions, err)
	}
}

func FuzzRunnerExecutionIdentityW8(f *testing.F) {
	f.Add("forged-runner", "v9.9.9", uint8(0))
	f.Add("", "v0.20.0", uint8(2))
	f.Add("server-runner", "", uint8(1))
	f.Fuzz(func(t *testing.T, runner, replacement string, field uint8) {
		unit, input := runnerIdentityFixtureW8()
		input.GrantedRunner.Name, input.Engine.Identity.Version = runner, replacement
		input.Accepted = nodehost.AcceptedRun{RunID: runner, SpecHash: replacement}
		input.Execution.Retry.Value = int(field)
		valid := field%4 == 0
		switch field % 4 {
		case 1:
			input.Entry.Engine.Identity.Version = replacement
			valid = replacement == unit.Engine.Version
		case 2:
			input.Entry.Engine.Identity.Pin.InputsDigest = replacement
			valid = replacement == unit.Engine.Pin.InputsDigest
		case 3:
			input.Entry.Engine.Identity.ManifestDigest = replacement
			valid = replacement == unit.Engine.ManifestDigest
		}
		called := false
		err := runIdentityW8(t, unit, input, func(got nodehost.NodeInput) {
			called = true
			if !reflect.DeepEqual(got.GrantedRunner, unit.Runner) || got.Engine.Identity != *unit.Engine || got.Accepted.RunID != unit.RunID || got.Accepted.SpecHash != string(unit.Accepted.Spec.Hash) || got.Accepted.PlanHash != string(unit.Accepted.PlanHash) || !reflect.DeepEqual(got.Execution, *unit.Execution) {
				t.Error("prepared identity replaced the server grant or verified selection")
			}
		})
		if called != valid || (err == nil) != valid {
			t.Fatal("execution identity crossed its award boundary")
		}
	})
}
