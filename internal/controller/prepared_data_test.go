package controller

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestPreparedDataHTTPPublishAndUnstartedAwardRead(t *testing.T) {
	testPreparedDataHTTPPublish(t, true)
}

func TestPreparedDataHTTPRefusesNativeTriggerPublisher(t *testing.T) {
	testPreparedDataHTTPPublish(t, false)
}

func testPreparedDataHTTPPublish(t *testing.T, cloud bool) {
	t.Helper()
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller.test", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	e, tenant := engineServerFixture(t, deployment.Config{}, func(s *Server) { s.WithDataObjects(objects) })
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Git.Ref = "main"
	run.Request.Request.Git.SHA = strings.Repeat("a", 40)
	run.Request.Request.Git.Repository = runspec.Repository{Key: "git:8.8.8.8/team/repo", Display: "8.8.8.8/team/repo"}
	run.Request.Request.Source.Ref = run.Request.Request.Git.SHA
	run.Request.Request.Source.Location = runspec.SourceLocation{RemoteURL: "https://8.8.8.8/team/repo", PipelineDir: ".sparkwing"}
	sealed := sealWith(t, run.Request, assertedProvenance(t, "ci", "main", run.Request.Request.Git.SHA))
	engineSQL(t, e.Store, "UPDATE runs SET git_branch='main',request_json=?,repo_key=?,repo_display=? WHERE team='a' AND id='run'", sealed, run.Request.Request.Git.Repository.Key, run.Request.Request.Git.Repository.Display)
	var ownerRaw string
	if cloud {
		engineSQL(t, e.Store, `UPDATE runs SET dispatch='controller'`)
		engineSQL(t, e.Store, `UPDATE triggers SET unit_kind='plan'`)
		ownerRaw = LauncherFixtureClaim(t, e.Store, "run", store.PlanNodeID)
		engineHTTPRoot(t, e, "a", "runner", []capability.Grant{capability.NodesClaim})
	} else {
		fetchedRunFixture(t, e.Store, "a", "run", "acme/repo")
		owner := engineRuntimeClaim(t, e, capability.UnitTrigger)
		if owner.Principal.Capability.Host == nil || owner.Principal.Capability.Host.Kind != capability.HostSelfHosted {
			t.Fatalf("expected self-hosted trigger: %+v", owner.Principal.Capability.Host)
		}
		ownerRaw = nativeClaimPresentation(t, e, owner)
	}
	h := e.Server.Handler()
	body := "prepared executable and source"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	w := engineClaimHTTP(t, h, ownerRaw, "POST", "/api/v1/data/upload", fmt.Sprintf(`{"digest":%q,"size":%d}`, digest, len(body)), 200)
	var upload dataUploadResponse
	if err = json.Unmarshal(w.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	put := httptest.NewRequest(http.MethodPut, upload.URL, strings.NewReader(body))
	for k, v := range upload.Headers {
		put.Header.Set(k, v)
	}
	putResult := httptest.NewRecorder()
	h.ServeHTTP(putResult, put)
	if putResult.Code != 201 {
		t.Fatalf("PUT: %d %s", putResult.Code, putResult.Body.String())
	}
	engineClaimHTTP(t, h, ownerRaw, "POST", "/api/v1/data/commit", fmt.Sprintf(`{"upload_id":%q}`, upload.UploadID), 200)
	if !cloud {
		engineClaimHTTP(t, h, ownerRaw, "POST", "/api/v1/data/records", fmt.Sprintf(`{"kind":"prepared","pipeline":"ci","name":"pipeline","key":"run","digest":%q}`, digest), http.StatusForbidden)
		return
	}
	engineClaimHTTP(t, h, ownerRaw, "POST", "/api/v1/data/records", fmt.Sprintf(`{"kind":"prepared","pipeline":"ci","name":"pipeline","key":"run","digest":%q}`, digest), 200)
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job","children":[{"pipeline":"deploy"}]}`), []byte(`{}`))
	claim, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256(claim.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	node := capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}}
	nodeRaw := nativeClaimPresentation(t, e, node)
	if !strings.Contains(node.Principal.Capability.Data.WriteRef, "host-") {
		t.Fatalf("fixture has no asserted host partition: %s", node.Principal.Capability.Data.WriteRef)
	}
	engineClaimHTTP(t, h, nodeRaw, "GET", "/api/v1/data/records?kind=prepared&pipeline=ci&name=pipeline&key=other-run", "", 403)
	w = engineClaimHTTP(t, h, nodeRaw, "GET", "/api/v1/data/records?kind=prepared&pipeline=ci&name=pipeline&key=run", "", 200)
	var hit controllerwire.DataRecordLookupResult
	if err = json.Unmarshal(w.Body.Bytes(), &hit); err != nil || !hit.Found || hit.Record == nil || string(hit.Record.Digest) != digest {
		t.Fatalf("lookup=%s: %v", w.Body.String(), err)
	}
	w = engineClaimHTTP(t, h, nodeRaw, "POST", "/api/v1/data/download", fmt.Sprintf(`{"digest":%q}`, digest), 200)
	var download DataDownloadResponse
	if err = json.Unmarshal(w.Body.Bytes(), &download); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRequest(http.MethodGet, download.URL, nil)
	for k, v := range download.Headers {
		get.Header.Set(k, v)
	}
	got := httptest.NewRecorder()
	h.ServeHTTP(got, get)
	if got.Code != 200 || got.Body.String() != body {
		t.Fatalf("GET=%d %q", got.Code, got.Body.String())
	}
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,outcome,finished_at,generation,expansion,spec_json,execution_json) VALUES ('a','run','gen','done','success',1,1,'expected',?,?)`, []byte(`{"id":"gen"}`), []byte(`{}`))
	engineSQL(t, e.Store, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status) VALUES ('a','run','gen',1,'pending')`)
	claim, err = e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitEval, RunID: "run", NodeID: "gen", SourceGeneration: 1}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err = e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256(claim.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	if principal.Capability.Host == nil || principal.Capability.Host.Kind != capability.HostSelfHosted || !slices.Contains(principal.Capability.Grants, capability.SourceRead) || slices.Contains(principal.Capability.Grants, capability.DataWrite) {
		t.Fatal("self-hosted eval must retain source acquisition without gaining data writes")
	}
	evalRaw := nativeClaimPresentation(t, e, capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run", NodeID: "gen"}})
	w = engineClaimHTTP(t, h, evalRaw, "GET", "/api/v1/data/records?kind=prepared&pipeline=ci&name=pipeline&key=run", "", 200)
	hit = controllerwire.DataRecordLookupResult{}
	if err = json.Unmarshal(w.Body.Bytes(), &hit); err != nil || !hit.Found || hit.Record == nil || string(hit.Record.Digest) != digest {
		t.Fatalf("eval lookup=%s: %v", w.Body.String(), err)
	}
	engineClaimHTTP(t, h, evalRaw, "POST", "/api/v1/data/download", fmt.Sprintf(`{"digest":%q}`, digest), 200)
	testPreparedChildHTTP(t, e, tenant, nodeRaw, ownerRaw, digest)
}

func testPreparedChildHTTP(t *testing.T, e *engineFixture, tenant *store.Tenant, parentRaw, planRaw, digest string) {
	t.Helper()
	h := e.Server.Handler()
	engineSQL(t, e.Store, `UPDATE runs SET lineage_json=? WHERE team='a' AND id='run'`, []byte(`{"admission_key":"capability:root","admission_subject":{"kind":"account","id":"fixture"}}`))
	engineAdmitBearer(t, e.Store, parentRaw)
	engineClaimHTTP(t, h, parentRaw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	parent, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	var submission controllerwire.CreateRunRequest
	if err := json.Unmarshal([]byte(childIntakeBody(t, "deploy", parent.Request.Request.Git)), &submission); err != nil {
		t.Fatal(err)
	}
	submission.Request.Source = parent.Request.Request.Source
	submission.Request.Source.Entrypoint = "ChildEntry"
	body, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	w := engineClaimHTTP(t, h, parentRaw, "POST", "/api/v1/runs/run/children", string(body), 201)
	var created controllerwire.CreateRunResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	child, err := tenant.ReadRun(t.Context(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(planRaw)))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: plan.Capability.Parent.ID, Target: &store.UnitRef{Kind: capability.UnitPlan, RunID: runspec.RunID(child.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := e.Store.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	ownerRaw := issued.Raw
	path := "/api/v1/data/records?kind=prepared&pipeline=deploy&name=pipeline&key=" + child.ID
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a',?,'stamp','pending',1,?,?)`, child.ID, []byte(`{"id":"stamp"}`), []byte(`{}`))
	nodeClaim, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(child.ID), NodeID: "stamp"}})
	if err != nil {
		t.Fatal(err)
	}
	nodePrincipal, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256(nodeClaim.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	nodeRaw := nativeClaimPresentation(t, e, capability.Authorized{Principal: nodePrincipal, Binding: nodePrincipal.Capability.Binding, Target: capability.Target{Team: "a", RunID: child.ID, NodeID: "stamp"}})
	w = engineClaimHTTP(t, h, nodeRaw, "GET", path, "", http.StatusOK)
	var nodeHit controllerwire.DataRecordLookupResult
	if err := json.Unmarshal(w.Body.Bytes(), &nodeHit); err != nil || !nodeHit.PreparedParent || !nodeHit.Found {
		t.Fatalf("child job did not inherit preparation: %s %v", w.Body.String(), err)
	}
	engineClaimHTTP(t, h, nodeRaw, "POST", "/api/v1/data/download", fmt.Sprintf(`{"digest":%q}`, digest), http.StatusOK)
	for _, mutation := range []string{"none", "revision", "pipeline revision", "repository", "checkout", "unrelated", "provenance"} {
		t.Run("child/"+mutation, func(t *testing.T) {
			request := child.Request
			switch mutation {
			case "revision":
				request.Request.Git.SHA = strings.Repeat("b", 40)
			case "pipeline revision":
				request.Request.Source.Ref = strings.Repeat("b", 40)
			case "repository":
				request.Request.Source.Location.RemoteURL = "https://github.com/elsewhere/repo"
			case "checkout":
				request.Request.Options.Checkout.Set = true
				request.Request.Options.Checkout.Value.Policy.Tags = new(true)
			case "unrelated":
				request.Request.Lineage.ParentRunID = ""
			case "provenance":
				request.Provenance = assertedProvenance(t, "deploy", "other", request.Request.Git.SHA)
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE team='a' AND id=?`, body, child.ID)
			if mutation == "provenance" {
				engineClaimHTTP(t, h, ownerRaw, "GET", path, "", http.StatusForbidden)
				engineClaimHTTP(t, h, ownerRaw, "POST", "/api/v1/data/download", fmt.Sprintf(`{"digest":%q}`, digest), http.StatusForbidden)
				return
			}
			w := engineClaimHTTP(t, h, ownerRaw, "GET", path, "", 200)
			var hit controllerwire.DataRecordLookupResult
			if err := json.Unmarshal(w.Body.Bytes(), &hit); err != nil {
				t.Fatal(err)
			}
			want := mutation == "none"
			if hit.Found != want || hit.PreparedParent != want || want && (hit.Record == nil || string(hit.Record.Digest) != digest || hit.Record.Key != child.ID || hit.Record.Pipeline != "deploy") {
				t.Fatalf("child lookup=%s; parent source=%+v, child source=%+v; parent checkout=%+v child checkout=%+v", w.Body.String(), parent.Request.Request.Source, request.Request.Source, parent.Request.Request.Options.Checkout.Value, request.Request.Options.Checkout.Value)
			}
			status := http.StatusForbidden
			if want {
				status = http.StatusOK
			}
			engineClaimHTTP(t, h, ownerRaw, "POST", "/api/v1/data/download", fmt.Sprintf(`{"digest":%q}`, digest), status)
		})
	}
}
