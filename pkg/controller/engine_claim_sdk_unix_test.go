//go:build unix

package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestClaimedRunDescribesRepositoryExampleOverSDKPipes(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, ".sparkwing")
	binary := filepath.Join(t.TempDir(), "pipeline")
	build := exec.CommandContext(t.Context(), "go", "-C", directory, "build", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build repository SDK: %v: %s", err, output)
	}
	doc, err := repos.Describe(t.Context(), pipelineentry.Binary(binary), root)
	if err != nil {
		t.Fatalf("plain describe: %v", err)
	}
	found := false
	for _, pipeline := range doc.Pipelines {
		if pipeline.Name == "example" {
			found = pipeline.Args != nil && len(pipeline.Args) == 0
		}
	}
	if !found {
		t.Fatal("repository example must declare an empty arguments array")
	}
	_, handler, bearer := controller.EngineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	response := controller.EngineClaimHTTP(t, handler, bearer, "POST", "/api/v1/runners/claim", `{"runner_version":"v1.2.3","kinds":["node"],"available_cores":1}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %s: %v", response.Body.String(), err)
	}
	input, err := nodehost.NewRunInput(*award.Claim)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := engine.ReadPin(directory, engine.GoSDK)
	if err != nil {
		t.Fatal(err)
	}
	selected := engine.Resolved{Identity: engine.Identity{Pin: pin, Version: "development"}, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}
	masker := secrets.NewMasker()
	processes := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: procenv.Base{Captured: os.Environ(), Masker: masker}}}
	planner := nodehost.SDKPlanner{
		Start: processes.StartPlanning, Masker: masker,
		EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
			return selected, nil
		},
		Log: func(_ context.Context, frame nodeproto.FrameLog) error { t.Log(frame.Message); return nil },
	}
	prepared, err := planner.Prepare(t.Context(), nodehost.PlanningProcessInput{
		Entry: &source.Entry{Command: []string{binary}, Directory: root, Engine: selected}, Entrypoint: "Example",
		Runner: input.GrantedRunner, Run: nodeproto.RunContext{RunID: input.RunID, Pipeline: "example"}, Args: map[string]string{},
	}, nodehost.PlanningInput{})
	if err != nil {
		t.Fatalf("run-path describe: %v", err)
	}
	if prepared.Description.Name != "example" || prepared.Description.Args == nil || len(prepared.Args) != 0 {
		t.Fatalf("run-path description: %+v", prepared)
	}
	raw, err := prepared.Functions.Plan(t.Context())
	if err != nil {
		t.Fatalf("run-path plan: %v", err)
	}
	var plan nodeproto.PlanDoc
	if err := json.Unmarshal(raw, &plan); err != nil || len(plan.Jobs) == 0 || plan.Jobs[0].ID != "configure" {
		t.Fatalf("repository example plan: %s: %v", raw, err)
	}
	acceptLocalExample(t, root, raw, selected)
}

func acceptLocalExample(t *testing.T, root string, raw []byte, selected engine.Resolved) {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	claimant, err := localcontroller.NewClaimant(st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := claimant.Close(); err != nil {
			t.Error(err)
		}
	})
	st.SetClaimProfileResolver(claimant)
	resolver, err := engine.NewResolver(engine.Config{BuildVersion: "development", CacheDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	e := &controller.EngineController{Store: st, RunnerPolicy: &controller.SignedRunnerAdmission{Resolver: resolver}}
	server := controller.New(st, nil).WithEngineController(e).WithPeerIdentity(func(*http.Request) capability.Subject {
		return capability.Subject{Kind: capability.SubjectLocal, ID: fmt.Sprintf("local:%d", os.Getuid())}
	})
	handler := server.Handler()
	repository, err := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: root})
	if err != nil {
		t.Fatal(err)
	}
	request := runspec.Request{
		Version: 1, Team: string(store.DefaultTeam), Pipeline: "example", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Origin: runspec.OriginLocal,
		Git: runspec.Git{Repository: repository}, Source: runspec.Source{Entrypoint: "Example", Location: runspec.SourceLocation{LocalCheckout: root, PipelineDir: ".sparkwing"}},
	}
	input, err := json.Marshal(controllerwire.CreateRunRequest{Request: request})
	if err != nil {
		t.Fatal(err)
	}
	response := controller.EngineClaimHTTP(t, handler, "", "POST", "/api/v1/runs", string(input), http.StatusCreated)
	var admitted controllerwire.CreateRunResponse
	if err := json.Unmarshal(response.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	claimBody := fmt.Sprintf(`{"runner_version":"development","kinds":["trigger"],"run_id":%q,"available_cores":%d}`, admitted.RunID, runtime.NumCPU())
	response = controller.EngineClaimHTTP(t, handler, "", "POST", "/api/v1/runners/claim", claimBody, http.StatusOK)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("local claim: %s: %v", response.Body, err)
	}
	project, err := os.ReadFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := settings.ProjectCheckoutDeclarations(project, ".sparkwing/sparkwing.yaml", "example")
	if err != nil {
		t.Fatal(err)
	}
	var plan nodeproto.PlanDoc
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan, Context: settings.Context{Local: true}})
	if err != nil {
		t.Fatal(err)
	}
	submission := controllerwire.PlanSubmission{Declarations: declarations, Plan: raw, Resolved: runspec.Resolved{Engine: selected.Identity, Entrypoint: "Example", Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}
	body, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+admitted.RunID+"/plan", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+award.Claim.Capability)
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != http.StatusOK {
		t.Fatalf("local example plan: HTTP %d %s", out.Code, out.Body)
	}

	tenant, err := st.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tenant.ReadSnapshot(t.Context(), admitted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	scheduling, err := snapshot.Scheduling()
	if err != nil {
		t.Fatal(err)
	}
	decision := store.Decide(scheduling)
	if snapshot.Run.Spec == nil || len(snapshot.Nodes) != len(plan.Jobs) || len(decision.Nodes) != 1 || decision.Nodes[0].NodeID != "configure" || decision.Nodes[0].Kind != store.DecisionReady {
		t.Fatalf("accepted example cannot schedule configure: %+v", decision)
	}
}
