//go:build rewrite_integration

package cluster

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const productionHTTPBoundary = "production HTTP boundary"

func TestProductionRunnerOwnerReachesNativeHost(t *testing.T) {
	testProductionNativeBinding(t, capability.UnitTrigger)
}

func TestProductionRunnerNodeReachesNativeHost(t *testing.T) {
	testProductionNativeBinding(t, capability.UnitNode)
}

func testProductionNativeBinding(t *testing.T, kind capability.UnitKind) {
	t.Helper()
	hosting, unit := productionNativeAward(t, kind)
	controller, err := client.NewEngineClient(client.EngineOptions{URL: hosting.ControllerURL, HTTP: hosting.HTTP, Bearer: fixedBearer(unit.Capability)})
	if err != nil {
		t.Fatal(err)
	}
	services, _, err := hosting.Bind(t.Context(), unit, controller)
	if services.Close != nil {
		t.Cleanup(func() {
			if err := services.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	var socket *net.OpError
	if err != nil && errors.As(err, &socket) && socket.Op == "listen" && errors.Is(err, syscall.EPERM) {
		t.Skipf("native host reached the sandbox socket boundary: %v", err)
	}
	if err != nil {
		t.Fatalf("production %s did not construct native services: %v", kind, err)
	}
	if _, _, _, err := nodehost.NewNativeHost(services.Config); err != nil {
		t.Fatalf("production %s returned incomplete native services: %v", kind, err)
	}
}

func productionNativeAward(t *testing.T, kind capability.UnitKind) (runners.Hosting, controllerwire.ClaimedUnit) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("SPARKWING_HOME", filepath.Join(home, "state"))
	raw := productionTestBearer("owner")
	binding := capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Holder: "runner", Generation: 1}
	purpose, grants := capability.PurposeTrigger, []capability.Grant{capability.TriggerHost}
	prefix := "/api/v1/runs/run/local-host"
	if kind == capability.UnitNode {
		binding.Kind, binding.NodeID = capability.BindingNode, "job"
		purpose, grants = capability.PurposeWork, []capability.Grant{capability.NodeReport, capability.DataRead}
		prefix += "/nodes/job"
	}
	principal := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}, Capability: capability.Capability{
		Prefix: "owner", Digest: sha256.Sum256([]byte(raw)), Team: "team", Audience: capability.Controller,
		Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Issuance: capability.Award,
		Unit: kind, UnitID: "unit", Purpose: purpose, Delivery: capability.DeliveryDirect,
		Binding: binding, Grants: grants, Data: &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "gh:42", RunID: "run"},
	}}
	now := time.Now().UTC()
	request := runspec.SealedRequest{RunID: "run", Hash: "request-hash", Request: runspec.Request{
		Team: "team", Git: runspec.Git{Repository: runspec.Repository{Key: "gh:42"}, SHA: strings.Repeat("a", 40)},
		Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/project", PipelineDir: ".sparkwing"}},
	}}
	unit := controllerwire.ClaimedUnit{Unit: kind, UnitID: "unit", Purpose: purpose, Delivery: capability.DeliveryDirect, RunID: "run", JobID: binding.NodeID, Binding: binding, Capability: raw, Request: request, LeaseExpiresAt: now.Add(time.Hour), Runner: nodeproto.RunnerIdentity{Name: "runner", Type: "runner"}}
	if kind == capability.UnitNode {
		unit.Engine = &engine.Identity{Version: "v1.2.3", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}, ManifestDigest: strings.Repeat("b", 64)}
		unit.Accepted = &controllerwire.PlanAccepted{Spec: runspec.Spec{Request: request, Hash: "spec-hash", Resolved: runspec.Resolved{Engine: *unit.Engine}}, Plan: json.RawMessage(`{"jobs":[{"id":"job"}]}`), PlanHash: "plan-hash"}
		unit.Execution = &runspec.JobExecutionSettings{}
	}
	connection := client.EngineOptions{URL: "http://controller.invalid", Bearer: fixedBearer(raw), HTTP: &http.Client{Transport: productionTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+raw {
			t.Fatal("native binding lost the owner award")
		}
		var response any
		switch r.Method + " " + r.URL.Path {
		case "POST " + prefix + "/authentication":
			response = localhostwire.LocalAuthentication{Snapshot: capability.AuthenticationSnapshot{
				Now: now, Authorities: []capability.AuthorityState{{Principal: principal, MembershipValid: true}},
				Binding: capability.BindingState{Current: binding, LeaseExpiresAt: now.Add(time.Hour)},
			}, Digests: [][32]byte{principal.Capability.Digest}}
		case "GET " + prefix + "/data-authority":
			response = localhostwire.LocalDataAuthority{Authority: blob.Authority{CapabilityRef: "owner", Principal: "runner", Repo: "gh:42"}}
		case "GET " + prefix + "/snapshot":
			snapshot := store.RunSnapshot{Run: store.RunRow{ID: "run", Team: "team", Request: request}}
			if unit.Accepted != nil {
				snapshot.Run.Spec, snapshot.Run.Plan = &unit.Accepted.Spec, unit.Accepted.Plan
				snapshot.Nodes = []store.NodeRow{{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Generation: binding.Generation, ClaimedBy: binding.Holder, CapabilityPrefix: principal.Capability.Prefix, Spec: json.RawMessage(`{"id":"job"}`), Execution: *unit.Execution}}
			}
			response = snapshot
		default:
			t.Fatalf("unexpected native binding request: %s %s", r.Method, r.URL)
		}
		body, err := json.Marshal(response)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body))}, err
	})}}
	return productionHosting(filepath.Join(home, "state"), "v1.2.3", connection), unit
}

func TestProductionCloudExchangeReachesAwardRenewal(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			_, unit := productionNativeAward(t, capability.UnitNode)
			unit.Unit = kind
			unit.Execution.Resources = runspec.Setting[runspec.Resources]{Optional: runspec.Optional[runspec.Resources]{Set: true, Value: runspec.Resources{
				Cores: runspec.Setting[float64]{Optional: runspec.Optional[float64]{Set: true, Value: 1}}, MemoryBytes: runspec.Setting[int64]{Optional: runspec.Optional[int64]{Set: true, Value: 1 << 30}},
			}}}
			expires := time.Now().Add(time.Hour)
			unit.ParentCapabilityExpiry = &expires
			binding := launcher.CloudJobBinding{Kind: launcher.CloudWork, UnitID: unit.UnitID, Team: unit.Request.Request.Team, RunID: unit.RunID, NodeID: unit.JobID, Holder: unit.Binding.Holder, Generation: unit.Binding.Generation, ParentCapabilityExpiry: expires}
			if kind == capability.UnitPlan {
				unit.Purpose, unit.JobID, unit.Binding.Kind, unit.Binding.NodeID = capability.PurposePlan, "", capability.BindingRun, ""
				unit.Accepted, unit.Engine = nil, nil
				binding.Kind, binding.NodeID = launcher.CloudPlan, ""
			} else if kind == capability.UnitEval {
				unit.Purpose, unit.SourceGeneration = capability.PurposeEval, unit.Binding.Generation
				unit.Binding.Expansion = &capability.LeaseRef{Kind: kind, ID: unit.UnitID, Holder: "eval-holder", Generation: 2}
				binding.Kind, binding.Holder, binding.Generation = launcher.CloudEval, "eval-holder", 2
				binding.SourceNodeID, binding.SourceGeneration = unit.JobID, unit.SourceGeneration
			}
			var spec source.Spec
			var err error
			if kind == capability.UnitPlan {
				spec, err = source.ForRequest(unit.Request)
			} else {
				spec, err = source.ForExecution(unit.Accepted.Spec)
			}
			if err != nil {
				t.Fatal(err)
			}
			award := launcher.CloudJobAward{Binding: binding, Engine: unit.Engine, Source: spec, Bootstrap: []byte(productionTestBearer("bootstrap")), CPUCores: 1, Memory: 1 << 30}
			boundary := errors.New("awarded unit renewal boundary")
			exchanges, renewals := 0, 0
			previousTransport := http.DefaultTransport
			http.DefaultTransport = productionTransport(func(r *http.Request) (*http.Response, error) {
				switch r.Method + " " + r.URL.Path {
				case "POST /api/v1/claims/exchange":
					exchanges++
					if r.Header.Get("Authorization") != "Bearer "+string(award.Bootstrap) {
						t.Fatal("exchange did not use the bootstrap")
					}
					body, err := json.Marshal(controllerwire.ExchangeResponse{Claim: unit})
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body))}, err
				case "POST /api/v1/claims/unit/heartbeat":
					renewals++
					if r.Header.Get("Authorization") != "Bearer "+unit.Capability {
						t.Fatal("renewal did not use the exchanged award")
					}
					return nil, boundary
				default:
					t.Fatalf("unexpected Cloud request: %s %s", r.Method, r.URL)
					return nil, boundary
				}
			})
			t.Cleanup(func() { http.DefaultTransport = previousTransport })
			directory := t.TempDir()
			metadata, err := json.Marshal(award)
			if err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string][]byte{"award.json": metadata, "bootstrap": award.Bootstrap} {
				if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"--controller=http://controller.invalid", "--run=run", fmt.Sprintf("--generation=%d", binding.Generation), "--bootstrap-file=" + filepath.Join(directory, "bootstrap"), "--source-dir=" + filepath.Join(directory, "source"), "--module-cache=" + filepath.Join(directory, "modules")}
			if unit.JobID != "" {
				args = append(args, "--job="+unit.JobID)
			}
			if err := ProductionRuntime("v1.2.3").Bootstrap.Run(t.Context(), "run-node", args); !errors.Is(err, boundary) || exchanges != 1 || renewals != 1 {
				t.Fatalf("production %s stopped before awarded renewal: exchanges=%d renewals=%d error=%v", kind, exchanges, renewals, err)
			}
		})
	}
}

func TestInstalledRunnerRuntimeComposition(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_RUNNER_ENTRYPOINT") == "1" {
		http.DefaultTransport = productionTransport(func(r *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("%s: %s %s%s", productionHTTPBoundary, r.Method, r.URL.Host, r.URL.Path)
		})
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"sparkwing-runner"}, os.Args[i+1:]...)
				MainWithVersion("v1.2.3")
				os.Exit(0)
			}
		}
		os.Exit(2)
	}
	options, award := bootstrapPlanFile(t)
	award.Source = source.Spec{RepoKey: "gh:42", RemoteURL: "https://github.com/acme/project", SHA: strings.Repeat("a", 40), PipelineDir: ".sparkwing", DefaultBranch: "main"}
	metadata, err := json.Marshal(award)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(options.BootstrapFile), "award.json"), metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	credentials := runnerCredentials(t)
	config := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(config, []byte("runner:\n  controller: https://controller.example\n  repos: [github.com/team/*]\n  credentials_dir: "+credentials+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	image := "example.com/image@sha256:" + strings.Repeat("a", 64)
	commands := map[string][]string{
		"start":        {"--config=" + config},
		"warm":         {"--controller=https://controller.example", "--credentials-dir=" + credentials},
		"launch":       {"--controller=https://controller.example", "--image=" + image, "--docker-image=" + image, "--cpu-ceiling=1", "--memory-ceiling=1Gi", "--credentials-dir=" + credentials},
		"fetch-source": bootstrapArguments(options),
		"run-node":     bootstrapArguments(options),
	}
	operations := map[string]string{
		"start":        "GET sparkwing.dev/versions.json",
		"warm":         "GET sparkwing.dev/versions.json",
		"fetch-source": "POST controller.example/api/v1/runs/run/source-credential",
		"run-node":     "POST controller.example/api/v1/claims/exchange",
	}
	for name, args := range commands {
		t.Run(name, func(t *testing.T) {
			child := exec.CommandContext(t.Context(), os.Args[0], append([]string{"-test.run=^TestInstalledRunnerRuntimeComposition$", "--", name}, args...)...)
			child.Env = append(os.Environ(), "SPARKWING_TEST_RUNNER_ENTRYPOINT=1", "HOME="+directory, "SPARKWING_HOME="+filepath.Join(directory, "state"), "XDG_CACHE_HOME="+filepath.Join(directory, "cache"))
			output, err := child.CombinedOutput()
			t.Logf("entrypoint result: %s", output)
			if err == nil {
				t.Fatal("runner exited without attempting its configured production operation")
			}
			for _, missing := range []string{"runner runtime is unavailable", "launcher runtime is unavailable", "requires the source-access service binding", "requires the shared host service binding"} {
				if strings.Contains(string(output), missing) {
					t.Fatalf("installed entrypoint has no production composition: %s", output)
				}
			}
			want := productionHTTPBoundary
			if operation, ok := operations[name]; ok {
				want += ": " + operation
			}
			if !strings.Contains(string(output), want) {
				t.Fatalf("installed entrypoint did not reach its production HTTP operation: %s", output)
			}
		})
	}
}
