//go:build unix

package controller_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type w9Pipeline struct {
	sw.Base
	name string
}

func (p w9Pipeline) Plan(_ context.Context, plan *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
	node := sw.Job(plan, p.name, func(context.Context) error { return nil })
	if p.name == "source" {
		node.Generate(func(_ context.Context, expansion *sw.Plan) error {
			if os.Getenv("BUILD_NOTE") != "private-cloud-environment" {
				return fmt.Errorf("submitted environment missing")
			}
			sw.Job(expansion, "generated", func(context.Context) error { return nil })
			return nil
		})
	}
	return nil
}

func TestW9MechanismSDKProcess(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "w9-sdk-process") {
		return
	}
	sw.Register("Build", func() sw.Pipeline[sw.NoInputs] { return w9Pipeline{name: "build"} })
	sw.Register("Deploy", func() sw.Pipeline[sw.NoInputs] { return w9Pipeline{name: "deploy"} })
	sw.Register("Generator", func() sw.Pipeline[sw.NoInputs] { return w9Pipeline{name: "source"} })
	requests, frames := os.NewFile(3, "requests"), os.NewFile(4, "frames")
	defer requests.Close()
	defer frames.Close()
	if err := sdkserve.Serve(t.Context(), requests, frames, sdkserve.Config{SDKVersion: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
}

func w9Entry(t *testing.T) (*source.Entry, runspec.Source, string) {
	t.Helper()
	repo, sha := sourcetest.Repository(t)
	code := runspec.Source{Ref: sha, Location: runspec.SourceLocation{LocalCheckout: repo, PipelineDir: ".sparkwing"}}
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "gh:1", SHA: sha, LocalCheckout: repo, PipelineDir: ".sparkwing"}, source.Options{Base: procenv.Base{Local: true, Captured: os.Environ()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	entry.Command = []string{exe, "-test.run=^TestW9MechanismSDKProcess$", "--", "w9-sdk-process"}
	entry.Engine = engine.Resolved{Identity: engine.Identity{Version: "v1.2.3", ManifestDigest: "verified-test-engine", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}}}
	return entry, code, sha
}

func w9Planner(entry *source.Entry, base procenv.Base) nodehost.SDKPlanner {
	sessions := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: base}}
	return nodehost.SDKPlanner{Start: sessions.StartPlanning, Masker: base.Masker, EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
		return entry.Engine, nil
	}, Log: func(context.Context, nodeproto.FrameLog) error { return nil }}
}

func TestNativeChildBuildToDeployPlansItsOwnSDKEntrypoint(t *testing.T) {
	entry, code, sha := w9Entry(t)
	e, handler, claim := controller.ChildIntakeFixture(t)
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	parent.Request.Request.Pipeline = "Build"
	parent.Request.Request.LocalSecretNames = []string{"TOKEN"}
	parent.Request.Request.Source = code
	parent.Request.Request.Source.Location = runspec.SourceLocation{}
	parent.Request.Request.Source.Entrypoint = "Build"
	parent.Request.Request.Git.SHA = sha
	raw, err := json.Marshal(parent.Request)
	if err != nil {
		t.Fatal(err)
	}
	controller.EngineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE id='run'`, raw)
	host, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: childOutputTransport{handler: handler}}, Bearer: func(context.Context) (string, error) { return claim, nil }}, "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	}()
	principal, err := host.Principal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, err := host.ProcessClient(t.Context(), principal, "job")
	if err != nil {
		t.Fatal(err)
	}
	id, err := process.StartChild(t.Context(), "Deploy", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := tenant.ReadRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	planner := w9Planner(entry, procenv.Base{Masker: secrets.NewMasker()})
	planned, err := planner.PrepareCheckout(t.Context(), nodehost.CheckoutPlanningInput{Entry: entry, Request: child.Request, Runner: nodeproto.RunnerIdentity{Name: "runner", Type: "runner", Labels: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planned.Plan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var doc nodeproto.PlanDoc
	if err := json.Unmarshal(plan, &doc); err != nil {
		t.Fatal(err)
	}
	if planned.Resolved.Entrypoint != "Deploy" || len(doc.Jobs) != 1 || doc.Jobs[0].ID != "deploy" {
		t.Fatalf("child planned parent: %s, entrypoint=%s", plan, planned.Resolved.Entrypoint)
	}
}

func TestCloudSubmittedEnvironmentGeneratorCompletesEvaluation(t *testing.T) {
	entry, code, sha := w9Entry(t)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e, _, _ := controller.EngineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim}, store.WithPolicies(func(*store.Store) (store.Policies, error) {
		return store.Policies{RunEnvironment: cipher}, nil
	}))
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string][]byte{"BUILD_NOTE": []byte("private-cloud-environment")}
	env, err := runspec.EnvironmentDigest(map[string]string{"BUILD_NOTE": string(values["BUILD_NOTE"])}, strings.Repeat("5", 64))
	if err != nil {
		t.Fatal(err)
	}
	input := runspec.Request{Version: 1, Team: "a", Pipeline: "Generator", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchController, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: sha}, Source: code, EnvironmentSource: runspec.EnvironmentSubmitted, Env: env, Options: runspec.Options{SubmitEnvAllow: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"BUILD_NOTE"}}}}}
	request, err := runspec.SealRequest(input, runspec.IntakeFacts{RunID: "run", AdmittedAt: time.Now().UTC(), Provenance: controller.AssertedProvenance(t, "Generator", "", sha)})
	if err != nil {
		t.Fatal(err)
	}
	base, err := procenv.WithSubmitted(procenv.Base{Masker: secrets.NewMasker()}, values, procenv.SubmitAcceptAll())
	if err != nil {
		t.Fatal(err)
	}
	planner := w9Planner(entry, base)
	prepared, err := planner.PrepareCheckout(t.Context(), nodehost.CheckoutPlanningInput{Request: request, Entry: entry, Runner: nodeproto.RunnerIdentity{Name: "launcher", Type: "cloud", Labels: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := prepared.Plan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var doc nodeproto.PlanDoc
	if err := json.Unmarshal(plan, &doc); err != nil {
		t.Fatal(err)
	}
	resolved, err := prepared.Resolve(doc)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := runspec.SealSpec(runspec.SealInput{Declarations: prepared.Declarations, Request: request, Resolved: resolved, Plan: plan, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	rawSpec, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	rawRequest, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	private, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := cipher.SealBound(secrets.PurposeRunEnvironment, "a", "run", "run-environment", false, true, string(private))
	if err != nil {
		t.Fatal(err)
	}
	controller.EngineSQL(t, e.Store, `UPDATE runs SET dispatch='controller',request_json=?,request_hash=?,spec_json=?,spec_hash=?,plan_json=?,plan_hash=?,sealed_at=1,plan_accepted_generation=1,submitted_env=? WHERE id='run'`, rawRequest, request.Hash, rawSpec, spec.Hash, plan, spec.PlanHash, envelope)
	controller.EngineSQL(t, e.Store, `UPDATE triggers SET unit_kind='plan'`)
	declaration, err := json.Marshal(doc.Jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	declarationHash, err := runspec.CanonicalHash(doc.Jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	controller.EngineSQL(t, e.Store, `UPDATE nodes SET node_id='source',status='done',outcome='success',finished_at=1,generation=7,expansion='expected',spec_json=?,spec_hash=? WHERE run_id='run'`, declaration, declarationHash)
	controller.EngineSQL(t, e.Store, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','controller',?,3)`, time.Now().Add(time.Hour).UnixNano())
	controller.EngineSQL(t, e.Store, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,owner_generation) VALUES ('a','run','source',7,'pending',3)`)
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100 * store.MicroCreditsPerCredit, Reference: "eval-test", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	launcher := controller.EngineHTTPRoot(t, e, "", "eval-launcher", []capability.Grant{capability.ClaimsLaunch})
	handler := controller.New(e.Store, nil).WithSecretsCipher(cipher).WithEngineController(e.EngineController).Handler()
	response := controller.EngineClaimHTTP(t, handler, launcher, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1.2.3","kinds":["eval"],"available_cores":1}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("eval award: %s %v", response.Body, err)
	}
	if len(award.Claim.SubmittedEnv) != 0 {
		t.Fatal("bootstrap disclosed environment")
	}
	response = controller.EngineClaimHTTP(t, handler, award.Claim.Capability, "POST", "/api/v1/claims/exchange", `{}`, 200)
	var exchange controllerwire.ExchangeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &exchange); err != nil {
		t.Fatal(err)
	}
	claim := exchange.Claim
	if string(claim.SubmittedEnv["BUILD_NOTE"]) != string(values["BUILD_NOTE"]) {
		t.Fatal("eval lost submitted environment")
	}
	principal, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(claim.Capability)))
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range principal.Capability.Grants {
		if grant == capability.SourceRead {
			t.Fatal("eval grants widened")
		}
	}
	base, err = procenv.WithSubmitted(procenv.Base{Masker: secrets.NewMasker()}, claim.SubmittedEnv, procenv.SubmitAcceptAll())
	if err != nil {
		t.Fatal(err)
	}
	sessions := nodehost.ProtocolSessionConfig{Revoke: func() error { return nil }, Environments: nodehost.SDKEnvironment{Store: tenant, Base: base}}
	accepted := nodehost.AcceptedRun{RunID: "run", Spec: rawSpec, SpecHash: string(spec.Hash), Plan: doc, PlanHash: string(spec.PlanHash)}
	process, err := sessions.StartEvaluation(t.Context(), nodehost.EvaluationInput{Entry: entry, Accepted: accepted, NodeID: "source", Generation: 7, SpecHash: string(declarationHash), GrantedRunner: claim.Runner})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := process.Close(); err != nil {
			t.Error(err)
		}
	}()
	ask := func(request nodeproto.Request) nodeproto.Reply {
		t.Helper()
		reply, err := process.Request(t.Context(), request)
		if err != nil || !reply.OK {
			t.Fatalf("SDK %s: %+v %v", request.Op, reply, err)
		}
		return reply
	}
	ask(nodeproto.Request{Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &nodeproto.EngineIdentity{Version: "v1.2.3"}, Runner: &claim.Runner, Binding: &nodeproto.ProcessBinding{RunID: "run", JobID: "source", Routes: nodeproto.ProcessRoutes{URL: "http://local", Bearer: claim.Capability}}})
	ask(nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: "Generator", Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: "run", Pipeline: "Generator"}})
	evaluated := ask(nodeproto.Request{Op: nodeproto.OpEval, Closure: doc.Jobs[0].Closures.Generate[0], Input: json.RawMessage(`null`)})
	var result nodeproto.EvalResult
	if err := json.Unmarshal(evaluated.Result, &result); err != nil || result.Error != nil {
		t.Fatalf("generator: %+v %v", result, err)
	}
	body, err := json.Marshal(struct {
		Jobs json.RawMessage `json:"jobs"`
	}{result.Value})
	if err != nil {
		t.Fatal(err)
	}
	registration, err := runspec.BuildSecretRegistration(runspec.Request{}, runspec.Resolved{})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := runspec.CanonicalHash(struct {
		Body         json.RawMessage
		Registration runspec.SecretRegistration
	}{body, registration})
	if err != nil {
		t.Fatal(err)
	}
	submission, err := json.Marshal(controllerwire.ExpansionSubmission{SourceGeneration: 7, Body: body, Registration: registration})
	if err != nil {
		t.Fatal(err)
	}
	controller.EngineClaimHTTP(t, handler, claim.Capability, "POST", "/api/v1/runs/run/jobs/source/expansion", string(submission), 200)
	row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "source"})
	if err != nil || row.Expansion != store.ExpansionAccepted || row.ExpansionDigest != string(digest) {
		t.Fatalf("generator did not settle: %+v %v", row, err)
	}
}
