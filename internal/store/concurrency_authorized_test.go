package store_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

var _ controller.ConcurrencyStore = (*store.Tenant)(nil)

func slotFixture(t *testing.T) *storeRecoveryFixture {
	t.Helper()
	return slotFixtureAt(t, storetest.New(t))
}

func slotFixtureAt(t *testing.T, target *storetest.Target) *storeRecoveryFixture {
	t.Helper()
	return newStoreRecoveryFixtureAt(t, true, target, func(doc *nodeproto.PlanDoc) {
		scope, capacity, cost, policy := "global", 3, 2, "queue"
		doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "deploy", Scope: &scope, Capacity: &capacity, Cost: &cost, OnLimit: &policy}}
	})
}

func slotAuthorization(t *testing.T, f *storeRecoveryFixture) capability.Authorized {
	t.Helper()
	route, ok := capability.LookupRoute("GET", "/api/v1/queue/groups/{group}")
	if !ok {
		t.Fatal("missing slot route")
	}
	row, err := f.tenant.ReadNode(t.Context(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	target := capability.Target{Team: string(f.tenant.Team()), RunID: f.ref.RunID, NodeID: f.ref.NodeID, SlotKey: "global:deploy", SlotRunID: f.ref.RunID, SlotNodeID: f.ref.NodeID, SlotGeneration: row.Generation}
	auth, err := capability.NewAuthenticator(f.s.AsOperator()).Authenticate(t.Context(), capability.Presentation{Raw: f.holderBearer, Class: capability.CredentialBearer, Evidence: capability.RequestEvidence{Target: target}}, route)
	if err != nil {
		t.Fatal(err)
	}
	a, err := capability.Authorize(t.Context(), f.s.AsOperator(), route, auth, target)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAuthorizedConcurrencyGroupDoesNotDiscloseUndeclaredReadEdgesProperty(t *testing.T) {
	f := slotFixture(t)
	competitor, err := f.capacityCompetitor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(88116, 7622))
	for i := range 32 {
		leaderRun := competitor
		if i%2 == 0 {
			leaderRun = fmt.Sprintf("foreign/%x", random.Uint64())
		}
		cache := fmt.Sprintf("memo/%x", random.Uint64())
		if _, err := f.s.DB().ExecContext(t.Context(), storetest.Rebind(f.s, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES (?,'global:deploy',3,1) ON CONFLICT(team,key) DO NOTHING`), f.tenant.Team()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.DB().ExecContext(t.Context(), storetest.Rebind(f.s, `INSERT INTO concurrency_waiters(team,key,run_id,node_id,holder_id,arrived_at,policy,cache_key_hash,leader_run_id,leader_node_id,generation,lease_expires_at) VALUES (?,'global:deploy',?,'job',?,?,'queue',?,?,'job',0,?)`), f.tenant.Team(), competitor, fmt.Sprintf("wait-%d", i), i+1, cache, leaderRun, time.Now().Add(time.Hour).UnixNano()); err != nil {
			t.Fatal(err)
		}
		state, err := f.tenant.ReadAuthorizedGroup(t.Context(), slotAuthorization(t, f), "global:deploy")
		if err != nil || len(state.Waiters) != 1 {
			t.Fatalf("group view unavailable: %+v, %v", state, err)
		}
		waiter := state.Waiters[0]
		if waiter.RunID != competitor || waiter.LeaderRunID != "" || waiter.LeaderNodeID != "" || waiter.CacheKeyHash != "" {
			t.Fatalf("group disclosed undeclared edge: %+v", waiter)
		}
		if _, err := f.s.DB().ExecContext(t.Context(), storetest.Rebind(f.s, `DELETE FROM concurrency_waiters WHERE team=? AND key='global:deploy'`), f.tenant.Team()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrencyGroupReadsSharedAdmissionAndRetiresSlotRoutes(t *testing.T) {
	f := slotFixture(t)
	admitted, err := f.tenant.AdmitCapacity(t.Context(), store.CapacityBinding{RunID: f.ref.RunID, NodeID: f.ref.NodeID, Generation: f.holder.Generation}, f.holder)
	if err != nil || admitted.Kind != store.AcquireGranted {
		t.Fatalf("admission: %+v %v", admitted, err)
	}
	handler := controller.New(f.s, nil).Handler()
	for _, route := range []struct{ method, suffix string }{
		{"POST", "/acquire"}, {"POST", "/heartbeat"}, {"POST", "/release"},
		{"POST", "/cancel-waiter"}, {"GET", "/holder"}, {"GET", "/resolve"},
		{"POST", "/force-release"}, {"GET", ""},
	} {
		request := httptest.NewRequest(route.method, "/api/v1/queue/groups/global:deploy"+route.suffix, strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer "+f.holderBearer)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if route.suffix != "" {
			if response.Code != http.StatusNotFound {
				t.Fatalf("retired %s: %d %s", route.suffix, response.Code, response.Body)
			}
			if _, exists := capability.LookupRoute(route.method, "/api/v1/queue/groups/{group}"+route.suffix); exists {
				t.Fatalf("retired policy %s", route.suffix)
			}
			continue
		}
		var group controllerwire.QueueGroup
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &group) != nil || group.Capacity != 3 || group.UsedCost != 2 || len(group.Holders) != 1 || group.Holders[0].HolderID != admitted.HolderID {
			t.Fatalf("group view: %d %s", response.Code, response.Body)
		}
	}
}
