package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestPrivateCapacityAdmitsPendingJobsAndPromotesAfterTerminalRelease(t *testing.T) {
	server, host, tenant, _ := localHostFixture(t)
	for _, id := range []string{"first", "second"} {
		engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run',?,'pending',?,?)`, id, []byte(`{"id":"`+id+`"}`), []byte(`{"concurrency":[{"group":"serial","scope":"run","capacity":1,"cost":1,"on_limit":"queue"}]}`))
	}
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
				body, err := json.Marshal(localhostwire.LocalCapacityRequest{Key: "global:serial", HolderID: test.run + "/build", Binding: store.CapacityBinding{RunID: test.run, NodeID: "build", Generation: claim.Node.Generation}, Acquire: &store.AcquireSlotRequest{Key: "global:serial", HolderID: test.run + "/build", RunID: test.run, NodeID: "build", Capacity: 1}})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest("POST", "/api/v1/runs/"+test.run+"/local-host/capacity/"+operation, bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+test.bearer)
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
