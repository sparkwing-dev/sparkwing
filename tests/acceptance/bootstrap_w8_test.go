package acceptance_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/cluster"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type bootstrapExchangeW8 struct {
	controllerwire.Controller
	exchange func(context.Context) (controllerwire.ExchangeResponse, error)
}

func (c bootstrapExchangeW8) Exchange(ctx context.Context) (controllerwire.ExchangeResponse, error) {
	return c.exchange(ctx)
}

type bootstrapHostW8 func(context.Context, controllerwire.ClaimedUnit) error

func (h bootstrapHostW8) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	return h(ctx, unit)
}

func writeBootstrapW8(t *testing.T, data map[string][]byte) string {
	t.Helper()
	directory := t.TempDir()
	for name, body := range data {
		if err := os.WriteFile(filepath.Join(directory, name), body, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(directory, "bootstrap")
}

func TestCloudBootstrapW8Components(t *testing.T) {
	for _, kind := range []launcher.CloudUnitKind{launcher.CloudPlan, launcher.CloudEval, launcher.CloudWork} {
		t.Run(string(kind), func(t *testing.T) {
			now, cfg, award := cloudFixture(kind)
			award.Source = source.Spec{RepoKey: "gh:1", RemoteURL: "https://github.com/team/repo.git", SHA: strings.Repeat("a", 40), PipelineDir: ".sparkwing", Engine: award.Engine}
			resources, err := launcher.BuildJob(t.Context(), now, cfg, award, &cloudBoundaryFake{})
			if err != nil {
				t.Fatal(err)
			}
			bootstrapFile := writeBootstrapW8(t, resources.BootstrapSecret.Data)
			restored, err := launcher.ReadBootstrapAward(bootstrapFile)
			if err != nil || !reflect.DeepEqual(restored, award) {
				t.Fatalf("private award transport lost the original inputs: %v", err)
			}
			binding := award.Binding
			unit := controllerwire.ClaimedUnit{
				Unit: capability.UnitNode, Purpose: capability.PurposeWork, Delivery: capability.DeliveryDirect,
				UnitID: binding.UnitID, RunID: binding.RunID, JobID: binding.NodeID, Capability: "private-work-capability",
				Binding:        capability.Binding{Kind: capability.BindingNode, RunID: binding.RunID, NodeID: binding.NodeID, Holder: binding.Holder, Generation: binding.Generation, Attempt: binding.Attempt},
				Request:        runspec.SealedRequest{RunID: binding.RunID, Request: runspec.Request{Team: binding.Team}},
				LeaseExpiresAt: now.Add(time.Minute), OwnerDeadline: &binding.OwnerDeadline, AttemptDeadline: &binding.AttemptDeadline, ParentCapabilityExpiry: &binding.ParentCapabilityExpiry,
				Engine: award.Engine,
				Execution: &runspec.JobExecutionSettings{Resources: runspec.Setting[runspec.Resources]{Optional: runspec.Optional[runspec.Resources]{Set: true, Value: runspec.Resources{
					Cores:       runspec.Setting[float64]{Optional: runspec.Optional[float64]{Set: true, Value: award.CPUCores}},
					MemoryBytes: runspec.Setting[int64]{Optional: runspec.Optional[int64]{Set: true, Value: award.Memory}},
				}}}},
			}
			unit.Request.Request.Git = runspec.Git{Repository: runspec.Repository{Key: runspec.RepoKey(award.Source.RepoKey)}, SHA: award.Source.SHA}
			unit.Request.Request.Source.Location = runspec.SourceLocation{RemoteURL: award.Source.RemoteURL, PipelineDir: award.Source.PipelineDir}
			switch kind {
			case launcher.CloudPlan:
				unit.Unit, unit.Purpose, unit.Binding.Kind = capability.UnitPlan, capability.PurposePlan, capability.BindingRun
			case launcher.CloudEval:
				unit.Unit, unit.Purpose, unit.SourceGeneration = capability.UnitEval, capability.PurposeEval, binding.SourceGeneration
				unit.Binding.Generation = binding.SourceGeneration
				unit.Binding.Expansion = &capability.LeaseRef{Kind: capability.UnitEval, ID: binding.UnitID, Holder: binding.Holder, Generation: binding.Generation}
			}
			if award.Engine != nil {
				unit.Accepted = &controllerwire.PlanAccepted{Spec: runspec.Spec{Request: unit.Request, Resolved: runspec.Resolved{Engine: *award.Engine}}}
			}
			unit.Runner.Name, unit.Runner.Type = "server-launcher", "cloud"
			var events []string
			host := launcher.BootstrapHost{ControllerURL: cfg.ControllerURL, NewController: func(options client.EngineOptions) (controllerwire.Controller, error) {
				bearer, err := options.Bearer(t.Context())
				if err != nil || bearer != string(award.Bootstrap) || options.Launcher {
					t.Fatal("bootstrap transport received unrelated authority")
				}
				return bootstrapExchangeW8{exchange: func(context.Context) (controllerwire.ExchangeResponse, error) {
					events = append(events, "exchange")
					return controllerwire.ExchangeResponse{Claim: unit}, nil
				}}, nil
			}, Host: bootstrapHostW8(func(_ context.Context, got controllerwire.ClaimedUnit) error {
				events = append(events, "host")
				if !reflect.DeepEqual(got, unit) {
					t.Fatal("host lost the original unit, accepted engine, granted runner or source generation")
				}
				return nil
			})}
			if err := host.Run(t.Context(), restored); err != nil || !reflect.DeepEqual(events, []string{"exchange", "host"}) {
				t.Fatalf("events=%v err=%v", events, err)
			}
		})
	}
}

func TestCloudBootstrapGrammarW8Components(t *testing.T) {
	for _, kind := range []launcher.CloudUnitKind{launcher.CloudEval, launcher.CloudWork} {
		t.Run(string(kind), func(t *testing.T) {
			now, cfg, award := cloudFixture(kind)
			resources, err := launcher.BuildJob(t.Context(), now, cfg, award, &cloudBoundaryFake{})
			if err != nil {
				t.Fatal(err)
			}
			pod := resources.Job.Spec.Template.Spec
			bootstrapFile := writeBootstrapW8(t, resources.BootstrapSecret.Data)
			for _, built := range [][]string{pod.InitContainers[1].Args, pod.Containers[0].Args} {
				args := append([]string(nil), built...)
				for i := range args {
					if args[i] == "--bootstrap-file" {
						args[i+1] = bootstrapFile
					}
				}
				called := false
				boundary := errors.New("service boundary")
				check := func(options cluster.BootstrapOptions) {
					called = true
					if options.RunID != award.Binding.RunID || options.JobID != award.Binding.NodeID || options.Generation != award.Binding.Generation || options.ControllerURL != cfg.ControllerURL || options.SourceDir != "/tmp/source" || options.ModuleCache != "/tmp/modules" || options.BootstrapFile != bootstrapFile {
						t.Fatal("bootstrap entrypoint changed the awarded selectors or private paths")
					}
				}
				runtime := cluster.BootstrapRuntime{
					Source: func(_ context.Context, options cluster.BootstrapOptions) (launcher.SourceInit, error) {
						check(options)
						return launcher.SourceInit{}, boundary
					},
					Host: func(_ context.Context, options cluster.BootstrapOptions) (launcher.BootstrapHost, func() error, error) {
						check(options)
						return launcher.BootstrapHost{}, nil, boundary
					},
				}
				err := runtime.Run(t.Context(), args[0], args[1:])
				if !errors.Is(err, boundary) || !called {
					t.Fatalf("built %s command did not reach its declared entrypoint: %v", args[0], err)
				}
			}
		})
	}
}
