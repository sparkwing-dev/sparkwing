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
	for _, change := range []string{"key", "capacity", "cost", "policy", "lease"} {
		t.Run(change, func(t *testing.T) {
			server, _, tenant, owner, bearer := localHostAwardFixture(t)
			acceptCapacityPlan(t, server, tenant, owner, "global", "queue", 1, 1, "build")
			seedOtherCapacityHolder(t, server, "global:serial")
			before, err := tenant.GetConcurrencyState(t.Context(), "global:serial")
			if err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"binding": store.CapacityBinding{RunID: "run", NodeID: "build"}}
			body[change] = map[string]any{"key": "global:undeclared", "capacity": 100, "cost": 2, "policy": "cancel_others", "lease": int64(time.Hour)}[change]
			if code := privateCapacityRequest(t, server, bearer, body); code != http.StatusBadRequest {
				t.Fatalf("changed declaration accepted: HTTP %d", code)
			}
			assertOtherCapacityUnchanged(t, tenant, "global:serial", before)
		})
	}
}

func seedOtherCapacityHolder(t *testing.T, server *serverFixture, key string) {
	t.Helper()
	if err := teststore.SeedRun(server.store, t.Context(), "a", store.Run{ID: "other", Pipeline: "ci", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','other','build','pending',?,?)`, []byte(`{"id":"build"}`), []byte(`{}`))
	engineSQL(t, server.store, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a',?,1,1)`, key)
	now := time.Now()
	engineSQL(t, server.store, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at) VALUES ('a',?,'other/build','other','build',?,?)`, key, now.UnixNano(), now.Add(time.Hour).UnixNano())
}

func privateCapacityRequest(t *testing.T, server *serverFixture, bearer string, body any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/runs/run/local-host/capacity/acquire", bytes.NewReader(raw))
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

func TestPrivateCapacityBindsBoxToPersistedAwardIdentity(t *testing.T) {
	for _, policy := range []string{"queue", "cancel_others"} {
		t.Run(policy, func(t *testing.T) {
			server, host, tenant, owner, bearer := localHostAwardFixture(t)
			acceptCapacityPlan(t, server, tenant, owner, "box", policy, 1, 1, "first", "second")
			otherKey := "box:13:other-machine:serial"
			seedOtherCapacityHolder(t, server, otherKey)
			before, err := tenant.GetConcurrencyState(t.Context(), otherKey)
			if err != nil {
				t.Fatal(err)
			}
			binding := store.CapacityBinding{RunID: "run", NodeID: "first"}
			// The declaration is valid; only the target machine has changed.
			for _, key := range []string{otherKey, "box:5:fresh:serial"} {
				if code := privateCapacityRequest(t, server, bearer, map[string]any{"key": key, "binding": binding}); code != http.StatusBadRequest {
					t.Fatalf("caller selected a box: HTTP %d", code)
				}
			}
			first, err := host.AdmitCapacity(t.Context(), binding, store.Actor{})
			if err != nil || first.Kind != store.AcquireGranted {
				t.Fatalf("bound box: %+v %v", first, err)
			}
			key := "box:6:runner:serial"
			initial, err := tenant.GetConcurrencyState(t.Context(), key)
			if err != nil || len(initial.Holders) != 1 {
				t.Fatalf("persisted machine key: %+v %v", initial, err)
			}
			// A retry adopts the continuously held slot; it does not refresh its lease.
			if again, err := host.AdmitCapacity(t.Context(), binding, store.Actor{}); err != nil || again.Kind != store.AcquireGranted || !again.LeaseExpiresAt.Equal(first.LeaseExpiresAt) {
				t.Fatalf("adoption: %+v %v", again, err)
			}
			after, err := tenant.GetConcurrencyState(t.Context(), key)
			if err != nil || !reflect.DeepEqual(initial, after) {
				t.Fatalf("adoption changed lease: %+v %+v %v", initial, after, err)
			}
			if policy == "queue" {
				binding.NodeID = "second"
				if queued, err := host.AdmitCapacity(t.Context(), binding, store.Actor{}); err != nil || queued.Kind != store.AcquireQueued {
					t.Fatalf("box contention bypassed: %+v %v", queued, err)
				}
				// Re-polling resolves the same waiter and cannot change its box.
				if queued, err := host.AdmitCapacity(t.Context(), binding, store.Actor{}); err != nil || queued.Kind != store.AcquireQueued {
					t.Fatalf("box waiter bypassed: %+v %v", queued, err)
				}
			}
			assertOtherCapacityUnchanged(t, tenant, otherKey, before)
		})
	}
}

func TestPrivateCapacityRefusesMissingPersistedHostIdentity(t *testing.T) {
	server, _, tenant, owner, bearer := localHostAwardFixture(t)
	acceptCapacityPlan(t, server, tenant, owner, "box", "queue", 1, 1, "build")
	var raw []byte
	if err := teststore.SQL(t, server.store).QueryRowContext(t.Context(), `SELECT authority_json FROM capabilities WHERE prefix='runner'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record store.CapabilityRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record.Value.Subject.Kind = capability.SubjectService
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.store, `UPDATE capabilities SET subject_kind='service',authority_json=? WHERE prefix='runner'`, raw)
	code := privateCapacityRequest(t, server, bearer, localhostwire.LocalCapacityRequest{Binding: store.CapacityBinding{RunID: "run", NodeID: "build"}})
	if code != http.StatusForbidden {
		t.Fatalf("missing host admitted: HTTP %d", code)
	}
	if state, err := tenant.GetConcurrencyState(t.Context(), "box:6:runner:serial"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing host wrote capacity: %+v %v", state, err)
	}
}

func TestPrivateCapacityUsesSealedDeclarationAndOwnerDeadline(t *testing.T) {
	server, host, tenant, owner := localHostFixture(t)
	acceptCapacityPlan(t, server, tenant, owner, "global", "queue", 3, 2, "build")
	until := time.Now().Add(time.Minute)
	engineSQL(t, server.store, `UPDATE triggers SET lease_expires_at=? WHERE team='a' AND id='run'`, until.UnixNano())
	result, err := host.AdmitCapacity(t.Context(), store.CapacityBinding{RunID: "run", NodeID: "build"}, store.Actor{})
	if err != nil || result.Kind != store.AcquireGranted || result.LeaseExpiresAt.After(until) {
		t.Fatalf("owner deadline: %+v %v", result, err)
	}
	state, err := tenant.GetConcurrencyState(t.Context(), "global:serial")
	if err != nil || state.Capacity != 3 || state.UsedCost != 2 || len(state.Holders) != 1 || state.Holders[0].HolderID != "run/build" || state.Holders[0].LeaseExpiresAt.After(until) {
		t.Fatalf("sealed acquisition: %+v %v", state, err)
	}
}

func TestPrivateCapacityAdmitsPendingJobsAndPromotesAfterTerminalRelease(t *testing.T) {
	server, host, tenant, owner := localHostFixture(t)
	acceptCapacityPlan(t, server, tenant, owner, "run", "queue", 1, 1, "first", "second")
	binding := store.CapacityBinding{RunID: "run", NodeID: "first"}
	if first, err := host.AdmitCapacity(t.Context(), binding, store.Actor{}); err != nil || first.Kind != store.AcquireGranted {
		t.Fatalf("first: %+v %v", first, err)
	}
	binding.NodeID = "second"
	if second, err := host.AdmitCapacity(t.Context(), binding, store.Actor{}); err != nil || second.Kind != store.AcquireQueued {
		t.Fatalf("second: %+v %v", second, err)
	}
	_, err := host.TransitionNode(t.Context(), store.NodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "first"}, To: store.NodeCancelled, Cause: store.CauseCancel, Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeCancelled}}})
	if err != nil {
		t.Fatal(err)
	}
	if promoted, err := host.AdmitCapacity(t.Context(), binding, store.Actor{}); err != nil || promoted.Kind != store.AcquireGranted {
		t.Fatalf("promotion: %+v %v", promoted, err)
	}
	state, err := tenant.GetConcurrencyState(t.Context(), "run:3:run:serial")
	if err != nil || len(state.Holders) != 1 || state.Holders[0].HolderID != "run/second" || len(state.Waiters) != 0 {
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
			for _, operation := range []string{"acquire"} {
				body, err := json.Marshal(localhostwire.LocalCapacityRequest{Binding: store.CapacityBinding{RunID: test.run, NodeID: "build", Generation: claim.Node.Generation}})
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

func TestPrivateCapacityAdmitsEverySealedGroup(t *testing.T) {
	server, host, tenant, owner := localHostFixture(t)
	acceptCapacityDocument(t, server, tenant, owner, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{
		ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}, Concurrency: []nodeproto.ConcurrencySpec{
			{Group: "global", Scope: new("global"), Capacity: new(3), Cost: new(2), OnLimit: new("queue")},
			{Group: "run", Scope: new("run"), Capacity: new(1), Cost: new(1), OnLimit: new("queue")},
			{Group: "machine", Scope: new("box"), Capacity: new(1), Cost: new(1), OnLimit: new("queue")},
		},
	}}})
	binding := store.CapacityBinding{RunID: "run", NodeID: "build"}
	result, err := host.AdmitCapacity(t.Context(), binding, store.Actor{})
	if err != nil || result.Kind != store.AcquireGranted {
		t.Fatalf("unit admission: %+v %v", result, err)
	}
	for key, cost := range map[string]int{"global:global": 2, "run:3:run:run": 1, "box:6:runner:machine": 1} {
		state, err := tenant.GetConcurrencyState(t.Context(), key)
		if err != nil || state.UsedCost != cost || len(state.Holders) != 1 || state.Holders[0].HolderID != "run/build" {
			t.Fatalf("omitted sealed group %s: %+v %v", key, state, err)
		}
	}
}

func TestPrivateCapacityRejectsAnotherMachinesBoxWithoutChangingItsHolder(t *testing.T) {
	server, _, tenant, owner, bearer := localHostAwardFixture(t)
	acceptCapacityPlan(t, server, tenant, owner, "box", "cancel_others", 1, 1, "build")
	key := "box:13:other-machine:serial"
	seedOtherCapacityHolder(t, server, key)
	before, err := tenant.GetConcurrencyState(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	code := privateCapacityRequest(t, server, bearer, map[string]any{
		"key": key, "binding": store.CapacityBinding{RunID: "run", NodeID: "build"},
	})
	if code != http.StatusBadRequest && code != http.StatusForbidden {
		t.Errorf("another machine's box accepted: HTTP %d", code)
	}
	assertOtherCapacityUnchanged(t, tenant, key, before)
}
