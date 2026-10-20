package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func acceptCapacityPlan(t *testing.T, server *serverFixture, tenant *store.Tenant, owner capability.Principal, scope string, capacity, cost int, ids ...string) {
	t.Helper()
	run, err := tenant.ReadRun(t.Context(), "run")
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
	engineSQL(t, server.store, `UPDATE runs SET request_json=?,request_hash=? WHERE team='a' AND id='run'`, sealed, run.Request.Hash)
	plan := nodeproto.PlanDoc{}
	for _, id := range ids {
		plan.Jobs = append(plan.Jobs, nodeproto.JobSpec{ID: id, Steps: []nodeproto.StepSpec{{ID: "body"}}, Concurrency: []nodeproto.ConcurrencySpec{{Group: "serial", Scope: &scope, Capacity: &capacity, Cost: &cost, OnLimit: new("queue")}}})
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: run.Request, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	actor := engineActor(capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}})
	_, err = tenant.AcceptPlan(t.Context(), store.PlanAcceptance{RunID: "run", Actor: actor, Seal: runspec.SealInput{Request: run.Request, Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Engine: engine.Identity{Version: "v1.0.0", ManifestDigest: "manifest", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}, Args: map[string]string{}, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPrivateCapacityOwnerCannotChangeAcceptedDeclaration(t *testing.T) {
	for _, change := range []string{"undeclared group", "capacity", "cost", "cancel_others", "lease"} {
		t.Run(change, func(t *testing.T) {
			server, _, tenant, owner, bearer := localHostAwardFixture(t)
			acceptCapacityPlan(t, server, tenant, owner, "global", 1, 1, "build")
			if err := teststore.SeedRun(server.store, t.Context(), "a", store.Run{ID: "other", Pipeline: "ci", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','other','build','pending',?,?)`, []byte(`{"id":"build"}`), []byte(`{}`))
			key := "global:serial"
			if change == "undeclared group" {
				key = "global:undeclared"
			}
			engineSQL(t, server.store, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a',?,1,1)`, key)
			now := time.Now()
			engineSQL(t, server.store, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at) VALUES ('a',?,'other/build','other','build',?,?)`, key, now.UnixNano(), now.Add(time.Hour).UnixNano())
			before, err := tenant.GetConcurrencyState(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			acquire := store.AcquireSlotRequest{Key: key, HolderID: "run/build", RunID: "run", NodeID: "build", Capacity: 1, Cost: 1, Policy: "queue"}
			switch change {
			case "capacity":
				acquire.Capacity = 100
			case "cost":
				acquire.Cost = 2
			case "cancel_others":
				acquire.Policy = "cancel_others"
			case "lease":
				acquire.Lease = time.Hour
			}
			body, err := json.Marshal(map[string]any{"key": key, "holder_id": "run/build", "binding": store.CapacityBinding{RunID: "run", NodeID: "build"}, "acquire": acquire})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/api/v1/runs/run/local-host/capacity/acquire", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+bearer)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest && w.Code != http.StatusForbidden {
				t.Errorf("changed declaration accepted: HTTP %d: %s", w.Code, w.Body)
			}
			if change != "lease" {
				actor := engineActor(capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}})
				if _, err := tenant.AcquireCapacity(t.Context(), acquire, store.CapacityBinding{RunID: "run", NodeID: "build"}, actor); err == nil {
					t.Error("owner store admission accepted a changed declaration")
				}
			}
			if change == "undeclared group" {
				body, err := json.Marshal(localhostwire.LocalCapacityRequest{Key: key, Binding: store.CapacityBinding{RunID: "run", NodeID: "build"}})
				if err != nil {
					t.Fatal(err)
				}
				req = httptest.NewRequest("POST", "/api/v1/runs/run/local-host/capacity/acquire", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+bearer)
				req.Header.Set("Content-Type", "application/json")
				w = httptest.NewRecorder()
				server.Handler().ServeHTTP(w, req)
				if w.Code != http.StatusForbidden {
					t.Errorf("undeclared group accepted: HTTP %d: %s", w.Code, w.Body)
				}
			}
			after, err := tenant.GetConcurrencyState(t.Context(), key)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refused request changed other run's capacity: before=%+v after=%+v error=%v", before, after, err)
			}
			other, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "other", NodeID: "build"})
			if err != nil || other.CancelRequestedAt != nil {
				t.Fatalf("refused request cancelled other run: %+v %v", other, err)
			}
		})
	}
}

func TestPrivateCapacityUsesSealedDeclarationAndOwnerDeadline(t *testing.T) {
	server, host, tenant, owner := localHostFixture(t)
	acceptCapacityPlan(t, server, tenant, owner, "global", 3, 2, "build")
	until := time.Now().Add(time.Minute)
	engineSQL(t, server.store, `UPDATE triggers SET lease_expires_at=? WHERE team='a' AND id='run'`, until.UnixNano())
	result, err := host.AcquireCapacity(t.Context(), store.AcquireSlotRequest{Key: "global:serial"}, store.CapacityBinding{RunID: "run", NodeID: "build"}, store.Actor{})
	if err != nil || result.Kind != store.AcquireGranted || result.LeaseExpiresAt.After(until) {
		t.Fatalf("owner deadline: %+v %v", result, err)
	}
	state, err := tenant.GetConcurrencyState(t.Context(), "global:serial")
	if err != nil || state.Capacity != 3 || state.UsedCost != 2 || len(state.Holders) != 1 || state.Holders[0].HolderID != "run/build" || state.Holders[0].LeaseExpiresAt.After(until) {
		t.Fatalf("sealed acquisition: %+v %v", state, err)
	}
}

func TestPrivateCapacityRejectsUndeclaredGroupForEveryOperation(t *testing.T) {
	for _, operation := range []string{"adopt", "acquire", "resolve"} {
		t.Run(operation, func(t *testing.T) {
			server, _, tenant, owner, bearer := localHostAwardFixture(t)
			acceptCapacityPlan(t, server, tenant, owner, "global", 1, 1, "build")
			if err := teststore.SeedRun(server.store, t.Context(), "a", store.Run{ID: "other", Pipeline: "ci", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			key := "global:undeclared"
			engineSQL(t, server.store, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a',?,1,1)`, key)
			now := time.Now()
			engineSQL(t, server.store, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at) VALUES ('a',?,'other/','other','',?,?)`, key, now.UnixNano(), now.Add(time.Hour).UnixNano())
			before, err := tenant.GetConcurrencyState(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(localhostwire.LocalCapacityRequest{Key: key, Binding: store.CapacityBinding{RunID: "run", NodeID: "build"}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/api/v1/runs/run/local-host/capacity/"+operation, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+bearer)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("undeclared group accepted: HTTP %d: %s", w.Code, w.Body)
			}
			after, err := tenant.GetConcurrencyState(t.Context(), key)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refused request changed other run's capacity: before=%+v after=%+v error=%v", before, after, err)
			}
			other, err := tenant.ReadRun(t.Context(), "other")
			if err != nil || other.CancelRequestedAt != nil {
				t.Fatalf("refused request cancelled other run: %+v %v", other, err)
			}
		})
	}
}

func TestPrivateCapacityAdmitsPendingJobsAndPromotesAfterTerminalRelease(t *testing.T) {
	server, host, tenant, owner := localHostFixture(t)
	acceptCapacityPlan(t, server, tenant, owner, "run", 1, 1, "first", "second")
	key := "run:3:run:serial"
	binding := store.CapacityBinding{RunID: "run", NodeID: "first"}
	actor := store.Actor{} // The transport derives authority from the bearer, never this value.
	adoption, err := host.AdoptCapacity(t.Context(), key, "run/first", binding, actor)
	if err != nil || adoption.Kind != store.AcquireAgain {
		t.Fatalf("fresh arrival: %+v %v", adoption, err)
	}
	acquire := func(id string) store.AcquireSlotResponse {
		t.Helper()
		result, err := host.AcquireCapacity(t.Context(), store.AcquireSlotRequest{Key: key, HolderID: "run/" + id, RunID: "run", NodeID: id, Capacity: 1, Cost: 1, Policy: "queue"}, store.CapacityBinding{RunID: "run", NodeID: id}, actor)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := acquire("first"); result.Kind != store.AcquireGranted {
		t.Fatalf("first: %+v", result)
	}
	binding.NodeID = "second"
	if result := acquire("second"); result.Kind != store.AcquireQueued {
		t.Fatalf("second bypassed capacity: %+v", result)
	}
	adoption, err = host.AdoptCapacity(t.Context(), key, "run/second", binding, actor)
	if err != nil || adoption.Kind != store.AdoptWaiter || adoption.Waiter.Binding != binding {
		t.Fatalf("waiter: %+v %v", adoption, err)
	}
	resolution, err := host.ResolveCapacityWaiter(t.Context(), key, binding, actor)
	if err != nil || resolution.Status != store.WaiterStillWaiting {
		t.Fatalf("premature promotion: %+v %v", resolution, err)
	}
	_, err = host.TransitionNode(t.Context(), store.NodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "first"}, To: store.NodeCancelled, Cause: store.CauseCancel, Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeCancelled}}})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err = host.ResolveCapacityWaiter(t.Context(), key, binding, actor)
	if err != nil || resolution.Status != store.WaiterPromoted || resolution.HolderID != "run/second" {
		t.Fatalf("promotion: %+v %v", resolution, err)
	}
	adoption, err = host.AdoptCapacity(t.Context(), key, "run/second", binding, actor)
	if err != nil || adoption.Kind != store.AdoptHolder || adoption.Holder.Binding != binding {
		t.Fatalf("holder: %+v %v", adoption, err)
	}
	state, err := tenant.GetConcurrencyState(t.Context(), key)
	if err != nil || len(state.Holders) != 1 || len(state.Waiters) != 0 {
		t.Fatalf("retained semaphore: %+v %v", state, err)
	}
}

func TestPrivateCapacityRejectsForeignTeamAndWrongUnit(t *testing.T) {
	server, _, tenant, owner, raw := localHostAwardFixture(t)
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','build','pending',1,?,?)`, []byte(`{"id":"build"}`), []byte(`{}`))
	a := capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
	claim, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "build"}, Actor: engineActor(a), Policy: store.SourceNone})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, bearer, run string }{
		{"wrong unit", string(claim.Bearer), "run"},
		{"root credential", "runner-secret", "run"},
		{"foreign team", raw, "foreign"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "foreign team" {
				engineSQL(t, server.store, `INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)`)
				if err := teststore.SeedRun(server.store, t.Context(), "foreign", store.Run{ID: "foreign", Pipeline: "ci", Status: "running"}); err != nil {
					t.Fatal(err)
				}
			}
			for _, operation := range []string{"adopt", "acquire", "resolve"} {
				body, err := json.Marshal(localhostwire.LocalCapacityRequest{Key: "global:serial", Binding: store.CapacityBinding{RunID: test.run, NodeID: "build", Generation: claim.Node.Generation}})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest("POST", "/api/v1/runs/"+test.run+"/local-host/capacity/"+operation, bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+test.bearer)
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				server.Handler().ServeHTTP(w, req)
				if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound && !(test.name == "root credential" && w.Code == http.StatusUnauthorized) {
					t.Fatalf("%s HTTP %d: %s", operation, w.Code, w.Body)
				}
			}
		})
	}
	state, err := tenant.GetConcurrencyState(t.Context(), "global:serial")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refusal mutated capacity: %+v %v", state, err)
	}
}
