package acceptance_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func testGoSDKVersionAndHardCut(t *testing.T) {
	bin, cleanup, err := acceptance.BuildGoSDKFixture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	t.Run("wire", func(t *testing.T) {
		recordLeaf(t)
		mismatch := startProtocolProbe(t, bin)
		hello := probeHello()
		hello.Protocol = 1
		sendProtocolProbe(t, mismatch, hello)
		requireProtocolCode(t, protocolProbeReply(t, mismatch, "hello"), apierr.ProtocolMismatch)
		if err := mismatch.Finish(); err != nil {
			t.Fatal(err)
		}

		process := startProtocolProbe(t, bin)
		raw, err := json.Marshal(probeHello())
		if err != nil {
			t.Fatal(err)
		}
		var additive map[string]any
		if err := json.Unmarshal(raw, &additive); err != nil {
			t.Fatal(err)
		}
		additive["future_optional"] = map[string]any{"enabled": true}
		sendProtocolProbe(t, process, additive)
		var negotiated nodeproto.HelloResult
		decodeProtocolProbe(t, protocolProbeReply(t, process, "hello"), &negotiated)
		if negotiated.Features == nil {
			t.Fatal("SDK omitted its optional feature advertisement")
		}
		sendProtocolProbe(t, process, map[string]any{"id": "removed", "op": "run_node", "job": "probe"})
		reply := protocolProbeReply(t, process, "removed")
		requireProtocolCode(t, reply, apierr.UnknownOp)
		if !strings.Contains(reply.Error.Message, "rebuild .sparkwing") {
			t.Fatal("unknown operation lacks a rebuild diagnostic")
		}
		if err := process.Finish(); err != nil {
			t.Fatal(err)
		}
		_, err = nodeproto.ValidatePlan(json.RawMessage(`{"protocol":2,"jobs":[]}`), nodeproto.ValidationContext{
			Placement: nodeproto.PlacementLocal, Secrets: noProbeSecrets{},
		})
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.PlanInvalid {
			t.Fatalf("removed plan version field = %v", err)
		}
	})
	t.Run("public-api", testGoSDKCompileSurface)
	t.Run("admission-class", testProtocolAdmissionClass)
}

func testProtocolAdmissionClass(t *testing.T) {
	for _, placement := range []nodeproto.Placement{
		nodeproto.PlacementLocal, nodeproto.PlacementLocalTriggered, nodeproto.PlacementRunnerHosted,
		nodeproto.PlacementLauncher, nodeproto.PlacementHere, nodeproto.PlacementGitHubActions,
	} {
		t.Run(string(placement), func(t *testing.T) {
			for _, class := range []string{"critical", "interactive", "normal", "batch", "", "urgent", "Batch", " batch "} {
				t.Run("class="+class, func(t *testing.T) {
					recordLeaf(t)
					raw, err := json.Marshal(map[string]any{"jobs": []any{}, "admission_class": class})
					if err != nil {
						t.Fatal(err)
					}
					plan, err := nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{
						Placement: placement, Secrets: noProbeSecrets{},
					})
					switch class {
					case "critical", "interactive", "normal", "batch":
						if err != nil || plan.AdmissionClass == nil || *plan.AdmissionClass != class {
							t.Fatalf("admission_class = %v, %v", plan.AdmissionClass, err)
						}
					default:
						var failure *apierr.Error
						if !errors.As(err, &failure) || failure.Code != apierr.PlanInvalid {
							t.Fatalf("invalid admission_class returned %v, want plan_invalid", err)
						}
						if !strings.Contains(failure.Message, "admission_class") {
							t.Fatalf("invalid class error omits field: %v", failure)
						}
					}
				})
			}
			plan, err := nodeproto.ValidatePlan(json.RawMessage(`{"jobs":[]}`), nodeproto.ValidationContext{
				Placement: placement, Secrets: noProbeSecrets{},
			})
			if err != nil || plan.AdmissionClass != nil {
				t.Fatalf("omitted admission_class = %v, %v", plan.AdmissionClass, err)
			}
		})
	}
}

type noProbeSecrets struct{}

func (noProbeSecrets) ContainsSecret(string) bool { return false }

func testGoSDKCompileSurface(t *testing.T) {
	const imports = "package main\nimport sw \"github.com/sparkwing-dev/sparkwing/sparkwing\"\n"
	dir := t.TempDir()
	compile := func(source string) (string, error) {
		t.Helper()
		path := filepath.Join(dir, "probe.go")
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(t.Context(), "go", "build", "-p", "4", "-o", filepath.Join(dir, "probe"), path)
		command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false")
		output, err := command.CombinedOutput()
		return string(output), err
	}
	valid := imports + `import "context"
func main() {
 p := sw.NewPlan()
 j := sw.Job(p, "job", func(context.Context) error { return nil })
 j.Memo(func(context.Context) (sw.MemoValue, error) { return sw.MemoKey("key"), nil }).Cache(sw.GoModules())
 j.Retry(1, sw.Backoff(0))
 _ = sw.StepError{StepID: "run"}
 _ = sw.NewConcurrencyGroup("pool", sw.ConcurrencyLimit{Capacity: 1}).String()
}
`
	if output, err := compile(valid); err != nil {
		t.Fatalf("final API does not compile: %v: %s", err, output)
	}
	for _, symbol := range []string{
		"sw.RetryAuto", "(*sw.JobNode).WhenRunner", "sw.ToolSlot", "sw.BoxToolBudget", "sw.Admitted",
		"(*sw.Approval).BeforeRun", "(*sw.Approval).AfterRun", "sw.RuntimePlumbing", "(*sw.JobNode).BeforeRunHooks",
	} {
		t.Run(symbol, func(t *testing.T) {
			recordLeaf(t)
			output, err := compile(imports + "func main() { _ = " + symbol + " }\n")
			name := symbol[strings.LastIndex(symbol, ".")+1:]
			if err == nil || !strings.Contains(output, name) || !strings.Contains(output, "undefined") {
				t.Fatalf("removed API %s: %v: %s", symbol, err, output)
			}
		})
	}
}
