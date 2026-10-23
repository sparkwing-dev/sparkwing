package nodehost

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestNodeExecutorCarriesConstructedIdentityThroughHello(t *testing.T) {
	input, state, deps, processes := executorFixture(t)
	runner := nodeproto.RunnerIdentity{Name: "granted", Type: "agent", Labels: []string{"name=granted", "os=linux"}}
	constructed, err := NewNodeInput(input.Accepted, state.row, input.Engine, runner)
	if err != nil {
		t.Fatal(err)
	}
	constructed.Entry = input.Entry
	t.Setenv("SPARKWING_RUNNER_NAME", "forged")
	t.Setenv("SPARKWING_RUNNER_TYPE", "cloud")
	t.Setenv("SPARKWING_RUNNER_LABELS", "pool=forged")
	t.Setenv("SPARKWING_TOOLCHAIN", "forged")
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), constructed); err != nil {
		t.Fatal(err)
	}
	if len(*processes) != 1 || len((*processes)[0].requests) == 0 {
		t.Fatal("execution sent no hello")
	}
	hello := (*processes)[0].requests[0]
	if hello.Op != nodeproto.OpHello || hello.Engine == nil || hello.Engine.Version != input.Engine.Identity.Version || !reflect.DeepEqual(hello.Runner, &runner) || !reflect.DeepEqual(hello.Binding, &constructed.RouteBinding) {
		t.Fatalf("hello changed the execution identity: %+v", hello)
	}
}

func TestNewNodeInputUsesPersistedAward(t *testing.T) {
	input, state, _, _ := executorFixture(t)
	t.Setenv("SPARKWING_RUNNER_NAME", "forged")
	t.Setenv("SPARKWING_RUNNER_TYPE", "cloud")
	t.Setenv("SPARKWING_RUNNER_LABELS", "pool=forged")
	t.Setenv("SPARKWING_TOOLCHAIN", "forged")
	runner := nodeproto.RunnerIdentity{Name: "granted", Type: "agent", Labels: []string{"pool=ci", "os=linux"}}
	got, err := NewNodeInput(input.Accepted, state.row, input.Engine, runner)
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeID != input.NodeID || got.Generation != input.Generation || got.Attempt != input.Attempt || got.SpecHash != input.SpecHash || got.Authority != input.Authority || !reflect.DeepEqual(got.Engine, input.Engine) || !reflect.DeepEqual(got.Execution, input.Execution) || !reflect.DeepEqual(got.GrantedRunner, runner) || got.RouteBinding.RunID != "run" || got.RouteBinding.JobID != "build" || got.RouteBinding.Routes.Bearer != "" {
		t.Fatalf("execution input lost its persisted award: %+v", got)
	}
	runner.Labels[0] = "forged"
	if got.GrantedRunner.Labels[0] != "pool=ci" {
		t.Fatal("execution input retained a mutable runner label slice")
	}
}

func TestNewNodeInputRefusesIncompleteOrChangedAwards(t *testing.T) {
	for _, mode := range []string{"run", "version", "manifest", "sdk", "claim", "holder", "generation", "attempt", "terminal", "hash", "settings", "runner_name", "runner_type", "runner_labels"} {
		t.Run(mode, func(t *testing.T) {
			input, state, _, _ := executorFixture(t)
			runner := nodeproto.RunnerIdentity{Name: "granted", Type: "agent", Labels: []string{}}
			switch mode {
			case "run":
				state.row.Ref.RunID = "other"
			case "version":
				input.Engine.Identity.Version = "v2.0.0"
			case "manifest":
				input.Engine.Identity.ManifestDigest = "other"
			case "sdk":
				input.Engine.Identity.Pin.Version = "v2.0.0"
			case "claim":
				state.row.CapabilityPrefix = ""
			case "holder":
				state.row.ClaimedBy = ""
			case "generation":
				state.row.Generation = 0
			case "attempt":
				state.row.Attempt = -1
			case "terminal":
				state.row.Status = "done"
			case "hash":
				state.row.SpecHash = ""
			case "settings":
				state.row.Execution.Retry.Value++
			case "runner_name":
				runner.Name = ""
			case "runner_type":
				runner.Type = ""
			case "runner_labels":
				runner.Labels = nil
			}
			if _, err := NewNodeInput(input.Accepted, state.row, input.Engine, runner); err == nil {
				t.Fatal("accepted an inconsistent execution award")
			}
		})
	}
}

func FuzzExecutionInputKeepsSealedEngine(f *testing.F) {
	f.Add("v2.0.0", "other", "v2.0.0")
	f.Add("", "", "")
	f.Fuzz(func(t *testing.T, version, manifest, pin string) {
		input, state, _, _ := executorFixture(t)
		original := input.Engine.Identity
		input.Engine.Identity.Version = version
		input.Engine.Identity.ManifestDigest = manifest
		input.Engine.Identity.Pin.Version = pin
		if input.Engine.Identity == original {
			return
		}
		runner := nodeproto.RunnerIdentity{Name: "granted", Type: "agent", Labels: []string{}}
		if _, err := NewNodeInput(input.Accepted, state.row, input.Engine, runner); err == nil {
			t.Fatal("execution replaced the sealed engine")
		}
	})
}
