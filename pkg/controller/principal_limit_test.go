package controller_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newBudgetServer(t *testing.T, b controller.RequestBudget) string {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ts := httptest.NewServer(controller.New(st, nil).WithRequestBudget(b).Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

func postClaim(t *testing.T, base string) *http.Response {
	t.Helper()
	resp, err := http.Post(base+"/api/v1/nodes/claim", "application/json",
		jsonBody(t, map[string]any{"holder_id": "runner-1"}))
	if err != nil {
		t.Fatalf("post claim: %v", err)
	}
	return resp
}

func TestRequestBudget_ShedsClaimsPastThePrincipalBudget(t *testing.T) {
	base := newBudgetServer(t, controller.RequestBudget{ClaimsPerMinute: 2})

	for i := range 2 {
		resp := postClaim(t, base)
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status != http.StatusNoContent {
			t.Fatalf("claim %d status=%d want 204", i, status)
		}
	}

	resp := postClaim(t, base)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("claim past the budget status=%d want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("shed claim carried no Retry-After")
	}
}

func TestRequestBudget_ShedsHeartbeatsPastThePrincipalBudget(t *testing.T) {
	base := newBudgetServer(t, controller.RequestBudget{HeartbeatsPerMinute: 1})

	first, err := http.Post(base+"/api/v1/agents/runner-1/heartbeat", "application/json",
		jsonBody(t, map[string]any{}))
	if err != nil {
		t.Fatalf("post heartbeat: %v", err)
	}
	_ = first.Body.Close()

	resp, err := http.Post(base+"/api/v1/agents/runner-1/heartbeat", "application/json",
		jsonBody(t, map[string]any{}))
	if err != nil {
		t.Fatalf("post heartbeat: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("heartbeat past the budget status=%d want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("shed heartbeat carried no Retry-After")
	}
}

func TestRequestBudget_ZeroLeavesTheRouteClassUnlimited(t *testing.T) {
	base := newBudgetServer(t, controller.RequestBudget{})
	for i := range 20 {
		resp := postClaim(t, base)
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status != http.StatusNoContent {
			t.Fatalf("claim %d status=%d want 204", i, status)
		}
	}
}

// A fleet of fifty runners claiming once a second and heartbeating every five
// spends this much a minute; the defaults must sit above it with room, or a
// healthy fleet would shed itself.
func TestRequestBudget_DefaultsClearAFleetOfFifty(t *testing.T) {
	const (
		fleet          = 50
		claimsAMinute  = fleet * 60
		beatsAMinute   = fleet * 12
		headroomFactor = 2
	)
	if controller.DefaultClaimsPerMinute < claimsAMinute*headroomFactor {
		t.Errorf("DefaultClaimsPerMinute=%d; want at least %d",
			controller.DefaultClaimsPerMinute, claimsAMinute*headroomFactor)
	}
	if controller.DefaultHeartbeatsPerMinute < beatsAMinute*headroomFactor {
		t.Errorf("DefaultHeartbeatsPerMinute=%d; want at least %d",
			controller.DefaultHeartbeatsPerMinute, beatsAMinute*headroomFactor)
	}
}
