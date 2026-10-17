package acceptance_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func bindRunnerIdentityW8(unit *controllerwire.ClaimedUnit) {
	identity := engine.Identity{Pin: engine.Pin{SDK: engine.GoSDK, Version: "v0.20.0", Manifest: "go.mod", InputsDigest: strings.Repeat("a", 64)}, Version: "v0.20.0", ManifestDigest: strings.Repeat("b", 64)}
	unit.Engine = &identity
	unit.Request = runspec.SealedRequest{RunID: unit.RunID, Hash: "request-hash", Request: runspec.Request{Team: "team"}}
	unit.Accepted = &controllerwire.PlanAccepted{Spec: runspec.Spec{Request: unit.Request, Hash: "spec-hash", Resolved: runspec.Resolved{Engine: identity}}, Plan: []byte(`{"jobs":[{"id":"job"}]}`), PlanHash: "plan-hash"}
	unit.Execution = &runspec.JobExecutionSettings{}
	unit.Runner = nodeproto.RunnerIdentity{Name: "granted-runner", Type: "runner", Labels: []string{"pool=granted", "linux"}}
}

func TestCloudWorkUsesRunnerExecutionIdentityW8(t *testing.T) {
	_, cfg, award := cloudFixture(launcher.CloudWork)
	unit := runnerAuthorityAwardW8(capability.UnitNode)
	binding := award.Binding
	unit.UnitID, unit.RunID, unit.JobID = binding.UnitID, binding.RunID, binding.NodeID
	unit.Binding = capability.Binding{Kind: capability.BindingNode, RunID: binding.RunID, NodeID: binding.NodeID, Holder: binding.Holder, Generation: binding.Generation, Attempt: binding.Attempt}
	unit.CloudResources = &match.Resources{Cores: award.CPUCores, MemoryBytes: award.Memory}
	unit.Engine = award.Engine
	unit.Accepted.Spec.Resolved.Engine = *award.Engine
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
			executions++
			if !reflect.DeepEqual(got, unit) {
				t.Fatal("Cloud execution lost its accepted engine, runner or fence")
			}
			return nil
		}),
	}
	if err := host.Run(t.Context(), award); err != nil || exchanges != 1 || executions != 1 {
		t.Fatalf("Cloud work identity: exchanges=%d executions=%d error=%v", exchanges, executions, err)
	}
}
