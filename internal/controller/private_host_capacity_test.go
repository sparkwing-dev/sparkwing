package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func acceptCapacityPlan(t *testing.T, server *serverFixture, tenant *store.Tenant, owner capability.Principal, scope, policy string, capacity, cost int, ids ...string) {
	t.Helper()
	plan := nodeproto.PlanDoc{}
	for _, id := range ids {
		plan.Jobs = append(plan.Jobs, nodeproto.JobSpec{ID: id, Steps: []nodeproto.StepSpec{{ID: "body"}}, Concurrency: []nodeproto.ConcurrencySpec{{Group: "serial", Scope: &scope, Capacity: &capacity, Cost: &cost, OnLimit: &policy}}})
	}
	acceptCapacityDocument(t, server, tenant, owner, plan)
}

func acceptCapacityDocument(t *testing.T, server *serverFixture, tenant *store.Tenant, owner capability.Principal, plan nodeproto.PlanDoc) {
	t.Helper()
	acceptCapacityDocumentForRun(t, server, tenant, owner, "run", plan)
}

func acceptCapacityDocumentForRun(t *testing.T, server *serverFixture, tenant *store.Tenant, owner capability.Principal, runID string, plan nodeproto.PlanDoc) {
	t.Helper()
	run, err := tenant.ReadRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Hash, err = runspec.CanonicalHash(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.store, `UPDATE runs SET request_json=?,request_hash=? WHERE team='a' AND id=?`, sealed, run.Request.Hash, runID)
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: run.Request, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	actor := engineActor(capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: runID}})
	_, err = tenant.AcceptPlan(t.Context(), store.PlanAcceptance{RunID: runID, Actor: actor, Seal: runspec.SealInput{Request: run.Request, Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Engine: engine.Identity{Version: "v1.0.0", ManifestDigest: "manifest", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}, Args: map[string]string{}, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}})
	if err != nil {
		t.Fatal(err)
	}
}

func seedOtherCapacityHolder(t *testing.T, server *serverFixture, key string) {
	t.Helper()
	if err := teststore.SeedRun(server.store, t.Context(), "a", store.Run{ID: "other", Pipeline: "ci", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.store, `UPDATE runs SET dispatch='controller',plan_accepted_generation=1 WHERE team='a' AND id='other'`)
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','other','build','pending',?,?)`, []byte(`{"id":"build"}`), []byte(`{}`))
	engineSQL(t, server.store, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a',?,1,1)`, key)
	now := time.Now()
	engineSQL(t, server.store, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at) VALUES ('a',?,'other/-','other','',?,?)`, key, now.UnixNano(), now.Add(time.Hour).UnixNano())
}

func privateCapacityRequest(t *testing.T, server *serverFixture, bearer string, body any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/runs/run/local-host/may-start", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	return w.Code
}

func assertOtherCapacityUnchanged(t *testing.T, tenant *store.Tenant, key string, before *store.ConcurrencyState) {
	t.Helper()
	after, err := tenant.GetConcurrencyState(t.Context(), key)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("changed other run's capacity: before=%+v after=%+v error=%v", before, after, err)
	}
	other, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "other", NodeID: "build"})
	if err != nil || other.CancelRequestedAt != nil {
		t.Fatalf("cancelled other run: %+v %v", other, err)
	}
}

func TestPrivateStartRequiresAwardAndUsesAcceptedGroupSet(t *testing.T) {
	server, ownerClient, tenant, owner, ownerBearer := localHostAwardFixture(t)
	acceptCapacityDocument(t, server, tenant, owner, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{
		ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}, Concurrency: []nodeproto.ConcurrencySpec{
			{Group: "global", Scope: new("global"), Capacity: new(3), Cost: new(2), OnLimit: new("queue")},
			{Group: "run", Scope: new("run"), Capacity: new(1), Cost: new(1), OnLimit: new("queue")},
			{Group: "machine", Scope: new("box"), Capacity: new(1), Cost: new(1), OnLimit: new("queue")},
		},
	}}})
	ownerActor := engineActor(capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}})
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "build"}
	if _, err := tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: ownerActor}); err != nil {
		t.Fatal(err)
	}
	award, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: ownerActor, Policy: store.SourceNone})
	if err != nil || award.Kind != store.Execute {
		t.Fatalf("award=%+v error=%v", award, err)
	}
	binding := store.CapacityBinding{RunID: "run", NodeID: "build", Generation: int64(award.Node.Generation)}
	if code := privateCapacityRequest(t, server, ownerBearer, localhostwire.LocalCapacityRequest{Binding: binding}); code != http.StatusForbidden && code != http.StatusConflict {
		t.Fatalf("owner obtained job permit: HTTP %d", code)
	}
	if err := ownerClient.AdoptClaim(t.Context(), award); err != nil {
		t.Fatal(err)
	}
	nodeClient, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{server.Handler()}}, Bearer: func(context.Context) (string, error) { return string(award.Bearer), nil }}, "run", "build")
	if err != nil {
		t.Fatal(err)
	}
	defer nodeClient.Close()
	actor := store.Actor{Kind: store.ActorHolder, NodeFence: runspec.NodeFence{RunID: "run", NodeID: "build", Holder: award.Node.ClaimedBy, Generation: award.Node.Generation}, CapabilityPrefix: award.Node.CapabilityPrefix}
	for _, host := range []*client.LocalHostClient{ownerClient, nodeClient} {
		response, err := host.MayStart(t.Context(), binding, actor)
		if err != nil || response.Kind != store.AcquireGranted {
			t.Fatalf("start=%+v error=%v", response, err)
		}
	}
	for key, cost := range map[string]int{"job:global:global": 2, "job:run:3:run:run": 1, "job:box:6:runner:machine": 1} {
		state, err := tenant.GetConcurrencyState(t.Context(), key)
		if err != nil || state.UsedCost != cost || len(state.Holders) != 1 {
			t.Fatalf("omitted sealed group %s: %+v error=%v", key, state, err)
		}
	}
	for _, change := range []string{"key", "capacity", "cost", "policy", "lease"} {
		body := map[string]any{"binding": binding, change: "caller-selected"}
		if code := privateCapacityRequest(t, server, string(award.Bearer), body); code != http.StatusBadRequest {
			t.Fatalf("caller selected %s: HTTP %d", change, code)
		}
	}
	for _, changed := range []store.CapacityBinding{{RunID: "run", NodeID: "wrong", Generation: binding.Generation}, {RunID: "run", NodeID: "build", Generation: binding.Generation + 1}, {RunID: "run", Generation: binding.Generation}} {
		code := privateCapacityRequest(t, server, string(award.Bearer), localhostwire.LocalCapacityRequest{Binding: changed})
		if code != http.StatusForbidden && code != http.StatusConflict && code != http.StatusNotFound {
			t.Fatalf("award widened to %+v: HTTP %d", changed, code)
		}
	}
}
