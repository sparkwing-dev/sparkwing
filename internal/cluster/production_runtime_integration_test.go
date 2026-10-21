//go:build rewrite_integration

package cluster

import (
	"bytes"
	"context"
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
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const productionHTTPBoundary = "production HTTP boundary"

func TestProductionRunnerCommandsClaimAndHost(t *testing.T) {
	for _, command := range []string{"start", "warm"} {
		for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
			t.Run(command+"/"+string(kind), func(t *testing.T) {
				hosting, unit := productionNativeAward(t, kind)
				credentials := runnerCredentials(t)
				config := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(config, []byte("runner:\n  controller: "+hosting.ControllerURL+"\n  repos: [github.com/acme/*]\n  max_concurrent: 1\n  credentials_dir: "+credentials+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				stop := errors.New("runner command probe completed")
				var mu sync.Mutex
				claims, renewals, private := 0, 0, 0
				previousTransport := http.DefaultTransport
				http.DefaultTransport = productionTransport(func(r *http.Request) (*http.Response, error) {
					mu.Lock()
					defer mu.Unlock()
					var response any
					switch r.Method + " " + r.URL.Path {
					case "POST /api/v1/runners/claim":
						claims++
						if r.Header.Get("Authorization") != "Bearer private-runner-credential" {
							t.Error("claim did not use the enrolled credential file")
						}
						if claims > 1 {
							cancel(stop)
							return nil, stop
						}
						var demand controllerwire.ClaimRequest
						if err := json.NewDecoder(r.Body).Decode(&demand); err != nil {
							return nil, err
						}
						if !slices.Equal(demand.Kinds, []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode}) || demand.RunnerVersion != "dev" {
							t.Errorf("command lost its unified claim selection: %+v", demand)
						}
						response = controllerwire.ClaimResponse{Claim: &unit}
					case "POST /api/v1/claims/unit/heartbeat":
						renewals++
						if r.Header.Get("Authorization") != "Bearer "+unit.Capability {
							t.Error("host renewal reused the enrollment credential")
						}
						response = controllerwire.RenewalResponse{ExpiresAt: unit.LeaseExpiresAt}
					default:
						if strings.Contains(r.URL.Path, "/local-host/") {
							private++
							return hosting.HTTP.Transport.RoundTrip(r)
						}
						return nil, fmt.Errorf("%s: %s %s", productionHTTPBoundary, r.Method, r.URL.Path)
					}
					body, err := json.Marshal(response)
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body))}, err
				})
				t.Cleanup(func() { http.DefaultTransport = previousTransport })
				args := []string{"--config=" + config}
				if command == "warm" {
					args = []string{"--controller=" + hosting.ControllerURL, "--repos=github.com/acme/*", "--credentials-dir=" + credentials, "--max-concurrent=1", "--max-claims-before-restart=1"}
				}
				err := ProductionRuntime("dev").Runner.Run(ctx, command, args, "dev")
				// safety: the award renews with its own bearer before source acquisition; the beat count is the keeper's.
				if claims == 0 || renewals == 0 || private == 0 {
					t.Fatalf("command did not claim, renew and bind its award: claims=%d renewals=%d private=%d error=%v", claims, renewals, private, err)
				}
				var socket *net.OpError
				if errors.As(err, &socket) && socket.Op == "listen" && errors.Is(socket.Err, syscall.EPERM) {
					t.Skipf("command reached native hosting after an enrolled claim: %v", err)
				}
				if err == nil || !strings.Contains(err.Error(), productionHTTPBoundary) {
					t.Fatalf("command stopped before native source acquisition: %v", err)
				}
			})
		}
	}
}

func TestProductionRunnerOwnerReachesNativeHost(t *testing.T) {
	testProductionNativeBinding(t, capability.UnitTrigger)
}

func TestProductionRunnerNodeReachesNativeHost(t *testing.T) {
	testProductionNativeBinding(t, capability.UnitNode)
}

func TestProductionCloudPlanAndEvalReachNativeHost(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) { testProductionNativeBinding(t, kind) })
	}
}

func testProductionNativeBinding(t *testing.T, kind capability.UnitKind) {
	t.Helper()
	hosting, unit := productionNativeAward(t, kind)
	machineConfig := filepath.Join(t.TempDir(), "invalid-machine.yaml")
	if err := os.WriteFile(machineConfig, []byte("machine: [invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", machineConfig)
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
	if kind == capability.UnitPlan {
		binding.Kind = capability.BindingRun
		purpose, grants = capability.PurposePlan, []capability.Grant{capability.NodeReport, capability.NodeResult, capability.DataRead}
	}
	if kind == capability.UnitEval {
		binding.Kind, binding.NodeID = capability.BindingNode, "job"
		binding.Expansion = &capability.LeaseRef{Kind: capability.UnitEval, ID: "unit", Holder: "eval-holder", Generation: 7}
		purpose, grants = capability.PurposeEval, []capability.Grant{capability.ExpansionSubmit, capability.DataRead}
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
	unit := controllerwire.ClaimedUnit{Unit: kind, UnitID: "unit", Purpose: purpose, Delivery: capability.DeliveryDirect, RunID: "run", JobID: binding.NodeID, Binding: binding, Capability: raw, Request: request, LeaseExpiresAt: now.Add(time.Hour), Runner: nodeproto.RunnerIdentity{Name: "runner", Type: "runner", Labels: []string{}}}
	if kind == capability.UnitNode || kind == capability.UnitEval {
		unit.Engine = &engine.Identity{Version: "v1.2.3", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}, ManifestDigest: strings.Repeat("b", 64)}
		unit.Accepted = &controllerwire.PlanAccepted{Spec: runspec.Spec{Request: request, Hash: "spec-hash", PlanHash: "plan-hash", Resolved: runspec.Resolved{Engine: *unit.Engine}}, Plan: json.RawMessage(`{"jobs":[{"id":"job"}]}`), PlanHash: "plan-hash"}
		unit.Execution = &runspec.JobExecutionSettings{}
	}
	if kind == capability.UnitEval {
		unit.SourceGeneration = binding.Generation
	}
	connection := client.EngineOptions{URL: "https://controller.invalid", Bearer: fixedBearer(raw), HTTP: &http.Client{Transport: productionTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+raw {
			return nil, errors.New("native binding lost the owner award")
		}
		var response any
		switch r.Method + " " + r.URL.Path {
		case "POST " + prefix + "/authentication":
			snapshot := capability.AuthenticationSnapshot{
				Now: now, Authorities: []capability.AuthorityState{{Principal: principal}},
				Binding: capability.BindingState{Current: binding, LeaseExpiresAt: now.Add(time.Hour)},
			}
			if kind == capability.UnitEval {
				until := now.Add(time.Hour)
				finished := now.Add(-time.Minute)
				snapshot.Binding.FinishedAt = &finished
				snapshot.Binding.Expansion = &capability.ExpansionState{Lease: *binding.Expansion, ExpiresAt: until, ControllerLeaseExpiresAt: &until}
			}
			response = localhostwire.LocalAuthentication{Snapshot: snapshot, Digests: [][32]byte{principal.Capability.Digest}}
		case "GET " + prefix + "/data-authority":
			response = localhostwire.LocalDataAuthority{Authority: blob.Authority{CapabilityRef: "owner", Principal: "runner", Repo: "gh:42"}}
		case "GET " + prefix + "/snapshot":
			snapshot := store.RunSnapshot{Run: store.RunRow{ID: "run", Team: "team", Request: request, Owner: &store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: "run", Holder: binding.Holder, Generation: binding.Generation}}}
			if unit.Accepted != nil {
				snapshot.Run.Spec, snapshot.Run.Plan = &unit.Accepted.Spec, unit.Accepted.Plan
				snapshot.Run.PlanHash = string(unit.Accepted.PlanHash)
				snapshot.Nodes = []store.NodeRow{productionAwardNode(unit, principal)}
			}
			response = snapshot
		case "GET /api/v1/runs/run/local-host/nodes/job":
			response = productionAwardNode(unit, principal)
		default:
			return nil, fmt.Errorf("unexpected native binding request: %s %s", r.Method, r.URL)
		}
		body, err := json.Marshal(response)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body))}, err
	})}}
	return productionHostingAt(filepath.Join(home, "state"), "v1.2.3", connection, "", "", false, procenv.SubmitAccept{}, nil), unit
}

func productionAwardNode(unit controllerwire.ClaimedUnit, principal capability.Principal) store.NodeRow {
	row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.JobID}, Status: store.StatusPending, Generation: unit.Binding.Generation, ClaimedBy: unit.Binding.Holder, CapabilityPrefix: principal.Capability.Prefix, Spec: json.RawMessage(`{"id":"job"}`), SpecHash: "job-hash", Execution: *unit.Execution}
	if unit.Unit == capability.UnitEval {
		row.Status, row.Outcome, row.Expansion = store.StatusDone, store.NodeSuccess, store.ExpansionExpected
		row.ClaimedBy, row.CapabilityPrefix = "", ""
	}
	return row
}

func TestProductionCloudExchangeReachesAwardRenewal(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			_, unit := productionNativeAward(t, capability.UnitNode)
			unit.Unit = kind
			unit.CloudResources = &match.Resources{Cores: 1, MemoryBytes: 1 << 30}
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
			award := launcher.CloudJobAward{LeaseExpiresAt: unit.LeaseExpiresAt, Binding: binding, Engine: unit.Engine, Source: spec, Bootstrap: []byte(productionTestBearer("bootstrap")), CPUCores: 1, Memory: 1 << 30}
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
			args := []string{"--controller=https://controller.invalid", "--run=run", fmt.Sprintf("--generation=%d", binding.Generation), "--bootstrap-file=" + filepath.Join(directory, "bootstrap"), "--source-dir=" + filepath.Join(directory, "source"), "--module-cache=" + filepath.Join(directory, "modules")}
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
			if r.URL.Host == "kubernetes.example" {
				fmt.Fprintln(os.Stderr, productionHTTPBoundary+": "+r.Method+" "+r.URL.Host+r.URL.Path)
				os.Exit(3)
			}
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
	kubeconfig := filepath.Join(directory, "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\ncurrent-context: test\ncontexts:\n- name: test\n  context:\n    cluster: kube\nclusters:\n- name: kube\n  cluster:\n    server: https://kubernetes.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
			child.Env = append(os.Environ(), "SPARKWING_TEST_RUNNER_ENTRYPOINT=1", "HOME="+directory, "SPARKWING_HOME="+filepath.Join(directory, "state"), "XDG_CACHE_HOME="+filepath.Join(directory, "cache"), "KUBECONFIG="+kubeconfig)
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

func TestProductionRunnerFetchesPrivateSourceByPlacement(t *testing.T) {
	for _, owner := range []bool{true, false} {
		t.Run(fmt.Sprintf("owner=%t", owner), func(t *testing.T) {
			hosting, unit := productionNativeAward(t, capability.UnitNode)
			unit.OwnerMachine = owner
			repository, sha := sourcetest.Repository(t)
			remote := "https://8.8.8.8/acme/project"
			log := sourcetest.Remote(t, map[string]string{remote: repository})
			helperLog := filepath.Join(t.TempDir(), "helper.log")
			sourcetest.Write(t, filepath.Join(os.Getenv("HOME"), ".gitconfig"), "[credential]\n\thelper = \"!f() { echo called >> "+helperLog+"; echo username=owner; echo password=owner-secret; }; f\"\n")
			controller, err := client.NewEngineClient(client.EngineOptions{URL: hosting.ControllerURL, HTTP: hosting.HTTP, Bearer: fixedBearer(unit.Capability)})
			if err != nil {
				t.Fatal(err)
			}
			services, _, err := hosting.Bind(t.Context(), unit, controller)
			if services.Close != nil {
				t.Cleanup(func() { _ = services.Close() })
			}
			var socket *net.OpError
			if err != nil && errors.As(err, &socket) && socket.Op == "listen" && errors.Is(err, syscall.EPERM) {
				t.Skipf("native host reached the sandbox socket boundary: %v", err)
			}
			if err != nil {
				t.Fatal(err)
			}
			entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/acme/project", RemoteURL: remote, SHA: sha}, services.Config.Source)
			if entry != nil {
				t.Cleanup(func() { _ = entry.Close() })
			}
			_, helperErr := os.Stat(helperLog)
			if owner {
				if err != nil || helperErr != nil {
					t.Fatalf("personal runner checkout = %v, owner helper used = %v", err, helperErr == nil)
				}
				if got := sourcetest.Git(t, entry.Directory, "rev-parse", "HEAD"); got != sha {
					t.Fatalf("checked out %s, want %s", got, sha)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "authorize source") || helperErr == nil {
				t.Fatalf("shared runner checkout = %v, owner helper used = %v", err, helperErr == nil)
			}
			if commands, _ := os.ReadFile(log); strings.Contains(string(commands), `"fetch"`) {
				t.Fatal("shared runner fetched without a controller-released credential")
			}
		})
	}
}

func TestProductionRunnerSchedulesEligibleJobWithOptionalComputeAdmission(t *testing.T) {
	for _, compute := range []bool{false, true} {
		t.Run(fmt.Sprintf("local_admission=%t", compute), func(t *testing.T) {
			base, unit := productionNativeAward(t, capability.UnitTrigger)
			home := filepath.Join(os.Getenv("HOME"), "state")
			t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "machine.yaml"))
			if compute {
				daemon, err := wingd.New(wingd.Config{Home: home, Version: "v1.2.3", Stay: true})
				if err != nil {
					t.Fatal(err)
				}
				ctx, stop := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- daemon.Run(ctx) }()
				select {
				case err := <-done:
					stop()
					t.Fatal(err)
				case <-daemon.Ready():
				}
				t.Cleanup(func() {
					stop()
					if err := <-done; err != nil {
						t.Error(err)
					}
				})
			}
			identity := engine.Identity{Version: "v1.2.3", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}, ManifestDigest: strings.Repeat("b", 64)}
			unit.Accepted = &controllerwire.PlanAccepted{Spec: runspec.Spec{Request: unit.Request, Hash: "spec-hash", PlanHash: "plan-hash", Resolved: runspec.Resolved{Engine: identity}}, PlanHash: "plan-hash", Plan: json.RawMessage(`{"jobs":[{"id":"job","concurrency":[{"group":"serial","scope":"run","capacity":1,"cost":1,"on_limit":"queue"}]}]}`)}
			unit.Accepted.Spec.Resolved.Settings.Options.Resources.Set = true
			unit.Accepted.Spec.Resolved.Settings.Options.Resources.Value.Cores.Set = true
			unit.Accepted.Spec.Resolved.Settings.Options.Resources.Value.Cores.Value = 0.01
			group := runspec.Concurrency{Group: "serial", Scope: "run", Capacity: 1, Cost: 1, OnLimit: "queue"}
			row := store.NodeRow{Execution: runspec.JobExecutionSettings{Concurrency: []runspec.Concurrency{group}}, Ref: controllerwire.NodeRef{RunID: unit.RunID, NodeID: "job"}, Status: store.StatusPending, SpecHash: "job-hash", Spec: json.RawMessage(`{"id":"job","concurrency":[{"group":"serial","scope":"run","capacity":1,"cost":1,"on_limit":"queue"}]}`)}
			row.OwnerExecution = &store.OwnerExecution{Owner: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}, Team: "team", Generation: unit.Binding.Generation, Node: row.Ref, NodeGeneration: row.Generation, SpecHash: row.SpecHash, Allowed: true}
			snapshot := store.RunSnapshot{Run: store.RunRow{ID: unit.RunID, Team: "team", State: store.RunRunning, Request: unit.Request, Spec: &unit.Accepted.Spec, Plan: unit.Accepted.Plan, PlanHash: string(unit.Accepted.PlanHash), Owner: &store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: unit.RunID, Holder: unit.Binding.Holder, Generation: unit.Binding.Generation}}, Nodes: []store.NodeRow{row}}
			scheduled := make(chan struct{})
			var once sync.Once
			var capacity sync.Mutex
			var held bool
			boundary := errors.New("eligible job reached self claim")
			connection := client.EngineOptions{URL: base.ControllerURL, HTTP: &http.Client{Transport: productionTransport(func(r *http.Request) (*http.Response, error) {
				var response any
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/runs/run/local-host/snapshot":
					response = snapshot
				case "POST /api/v1/claims/unit/heartbeat":
					response = controllerwire.RenewalResponse{ExpiresAt: unit.LeaseExpiresAt}
				case "POST /api/v1/runs/run/local-host/nodes/job/transition":
					var change localhostwire.LocalNodeTransition
					if err := json.NewDecoder(r.Body).Decode(&change); err != nil {
						return nil, err
					}
					if change.To != store.NodeReady {
						return nil, fmt.Errorf("unexpected scheduled transition: %+v", change)
					}
					response = store.Applied{}
				case "POST /api/v1/runs/run/local-host/capacity/acquire":
					var request localhostwire.LocalCapacityRequest
					decoder := json.NewDecoder(r.Body)
					decoder.DisallowUnknownFields()
					if err := decoder.Decode(&request); err != nil {
						return nil, err
					}
					if request.Binding != (store.CapacityBinding{RunID: unit.RunID, NodeID: "job", Generation: row.Generation}) {
						return nil, fmt.Errorf("capacity changed its job or group: %+v", request)
					}
					capacity.Lock()
					defer capacity.Unlock()
					held = true
					response = store.AcquireSlotResponse{Kind: store.AcquireGranted, HolderID: "run/job", LeaseExpiresAt: unit.LeaseExpiresAt}
				case "POST /api/v1/runs/run/jobs/job/reconcile":
					capacity.Lock()
					admitted := held
					capacity.Unlock()
					if admitted {
						return nil, errors.New("job acquired concurrency before receiving its award")
					}
					if compute {
						state, err := wingdclient.Query(r.Context(), wingdclient.Options{Home: home, Version: "v1.2.3"})
						if err != nil {
							return nil, err
						}
						if !slices.ContainsFunc(state.Holders, func(holder wingwire.Holder) bool {
							return holder.DisplayRunID == unit.RunID && holder.Resources.Cores > 0
						}) {
							return nil, fmt.Errorf("job scheduled without its machine compute lease: %+v", state.Holders)
						}
					}
					once.Do(func() { close(scheduled) })
					return nil, boundary
				default:
					if strings.Contains(r.URL.Path, "events") {
						select {
						case <-scheduled:
							return nil, boundary
						case <-r.Context().Done():
							return nil, context.Cause(r.Context())
						}
					}
					return base.HTTP.Transport.RoundTrip(r)
				}
				body, err := json.Marshal(response)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body))}, err
			})}}
			hosting := productionHostingAt(home, "v1.2.3", connection, "", "", compute, procenv.SubmitAccept{}, nil)
			controller, err := client.NewEngineClient(client.EngineOptions{URL: connection.URL, HTTP: connection.HTTP, Bearer: fixedBearer(unit.Capability)})
			if err != nil {
				t.Fatal(err)
			}
			services, _, err := hosting.Bind(t.Context(), unit, controller)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := services.Close(); err != nil {
					t.Error(err)
				}
			})
			// The accepted resume needs no SDK planning. Source acquisition is an external boundary;
			// all scheduling, placement and admission callbacks use production composition.
			services.Config.Owner.Acquire = func(context.Context, nodehost.RunInput) (*source.Entry, error) {
				return &source.Entry{Directory: t.TempDir()}, nil
			}
			owner, _, _, err := nodehost.NewNativeHost(services.Config)
			if err != nil {
				t.Fatal(err)
			}
			input, err := nodehost.NewRunInput(unit)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err = owner.Run(ctx, input)
			select {
			case <-scheduled:
			default:
				t.Fatalf("eligible job never reached scheduling self claim: %v", err)
			}
			if err == nil || !strings.Contains(err.Error(), boundary.Error()) {
				t.Fatalf("scheduling stopped at another boundary: %v", err)
			}
		})
	}
}
