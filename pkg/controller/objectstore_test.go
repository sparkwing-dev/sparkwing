package controller_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestObjectStoreBreaker_RouteReportsEveryClass(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	resp := mustGet(t, base+"/api/v1/object-store/breaker")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("breaker status=%d want 200", resp.StatusCode)
	}
	var body struct {
		Enabled bool   `json:"enabled"`
		Reset   string `json:"reset"`
		Tripped bool   `json:"tripped"`
		Classes []struct {
			Class     string `json:"class"`
			PerMinute int    `json:"per_minute"`
			PerDay    int    `json:"per_day"`
			Tripped   bool   `json:"tripped"`
		} `json:"classes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Classes) != 4 {
		t.Fatalf("breaker reports %d classes, want put, get, list and delete", len(body.Classes))
	}
	if body.Tripped {
		t.Error("a fresh controller reports its object-store breaker as tripped")
	}
	for _, c := range body.Classes {
		if c.PerMinute <= 0 || c.PerDay <= 0 {
			t.Errorf("class %q carries no budget: per_minute=%d per_day=%d", c.Class, c.PerMinute, c.PerDay)
		}
	}
}

func TestObjectStoreResetBreaker_ClearsNothingWhenNothingTripped(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	resp, err := http.Post(base+"/api/v1/object-store/reset-breaker", "application/json", nil)
	if err != nil {
		t.Fatalf("post reset-breaker: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset-breaker status=%d want 200", resp.StatusCode)
	}
	var body struct {
		Cleared []string `json:"cleared"`
		Tripped bool     `json:"tripped"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Cleared) != 0 {
		t.Errorf("reset cleared %v on a controller with nothing tripped", body.Cleared)
	}
	if body.Tripped {
		t.Error("the breaker reports itself tripped straight after a reset")
	}
}

func TestObjectStoreResetBreaker_NeedsAdminScope(t *testing.T) {
	base, _, cleanup := newAuthedTestServer(t)
	defer cleanup()

	resp, err := http.Post(base+"/api/v1/object-store/reset-breaker", "application/json", nil)
	if err != nil {
		t.Fatalf("post reset-breaker: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated reset got %d, want 401", resp.StatusCode)
	}
}

func TestObjectStoreHealth_NamesTrippedClassesWithoutTheirLimits(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	resp := mustGet(t, base+"/api/v1/health")
	defer resp.Body.Close()
	var body struct {
		ObjectStore map[string]any `json:"object_store"`
		Problems    []string       `json:"problems"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ObjectStore["tripped"] != false {
		t.Fatalf("a fresh controller reports tripped=%v", body.ObjectStore["tripped"])
	}
	if _, ok := body.ObjectStore["per_minute"]; ok {
		t.Error("the unauthenticated health route carries the configured limits")
	}
	for _, p := range body.Problems {
		if strings.ContainsAny(p, "0123456789") && strings.Contains(p, "object-store") {
			t.Errorf("an object-store health problem carries a number: %q", p)
		}
	}
}

func TestMetrics_ObjectStoreBudgetIsExported(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	body := scrape(t, base)
	for _, want := range []string{
		`sparkwing_object_store_requests_total{class="put",outcome="allowed"}`,
		`sparkwing_object_store_requests_total{class="get",outcome="refused"}`,
		`sparkwing_object_store_trips_total{class="list"}`,
		`sparkwing_object_store_tripped{class="delete"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}
