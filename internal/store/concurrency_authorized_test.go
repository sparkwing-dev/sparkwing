package store_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
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

func TestConcurrencyGroupReadsSharedAdmissionAndRetiresSlotRoutes(t *testing.T) {
	f := slotFixture(t)
	admitted, err := f.tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}, f.holder)
	if err != nil || admitted.Kind != runstate.AcquireGranted {
		t.Fatalf("admission: %+v %v", admitted, err)
	}
	handler := controller.New(f.s, nil).Handler()
	for _, route := range []struct{ method, suffix string }{
		{"POST", "/acquire"},
		{"POST", "/heartbeat"},
		{"POST", "/release"},
		{"POST", "/cancel-waiter"},
		{"GET", "/holder"},
		{"GET", "/resolve"},
		{"POST", "/force-release"},
		{"GET", ""},
	} {
		request := httptest.NewRequest(route.method, "/api/v1/queue/groups/job:global:deploy"+route.suffix, strings.NewReader(`{}`))
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
