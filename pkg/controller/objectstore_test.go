package controller_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
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

func freezeBucket(t *testing.T, limit objectguard.CeilingLimit, bytes, objects int64) *objectguard.Ceiling {
	t.Helper()
	limiter, err := objectguard.Shared()
	if err != nil {
		t.Fatalf("shared limiter: %v", err)
	}
	ceiling := limiter.Ceiling()
	t.Cleanup(func() { ceiling.Configure(objectguard.CeilingConfig{}) })
	ceiling.Configure(objectguard.CeilingConfig{Limit: limit})
	ceiling.Observe(objectguard.Usage{Bytes: bytes, Objects: objects})
	return ceiling
}

func TestObjectStoreBreaker_ReportsAnUnlimitedCeilingByDefault(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	resp := mustGet(t, base+"/api/v1/object-store/breaker")
	defer resp.Body.Close()
	var body struct {
		Ceiling struct {
			Enforced bool  `json:"enforced"`
			Frozen   bool  `json:"frozen"`
			MaxBytes int64 `json:"max_bytes"`
		} `json:"ceiling"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Ceiling.Enforced || body.Ceiling.Frozen || body.Ceiling.MaxBytes != 0 {
		t.Errorf("a controller that configures no ceiling reports %+v", body.Ceiling)
	}
}

func TestObjectStoreBreaker_ReportsAFrozenCeilingWithItsTotals(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()
	freezeBucket(t, objectguard.CeilingLimit{MaxBytes: 1000}, 4096, 12)

	resp := mustGet(t, base+"/api/v1/object-store/breaker")
	defer resp.Body.Close()
	var body struct {
		Ceiling struct {
			Enforced bool   `json:"enforced"`
			Frozen   bool   `json:"frozen"`
			Reason   string `json:"frozen_reason"`
			Bytes    int64  `json:"bytes"`
			Objects  int64  `json:"objects"`
			MaxBytes int64  `json:"max_bytes"`
		} `json:"ceiling"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Ceiling.Enforced || !body.Ceiling.Frozen {
		t.Fatalf("a bucket over its ceiling reports %+v", body.Ceiling)
	}
	if body.Ceiling.Reason != "bytes" || body.Ceiling.Bytes != 4096 || body.Ceiling.MaxBytes != 1000 {
		t.Errorf("the admin route reports %+v, want 4096 bytes against a 1000-byte ceiling", body.Ceiling)
	}
	if body.Ceiling.Objects != 12 {
		t.Errorf("the admin route reports %d objects, want 12", body.Ceiling.Objects)
	}
}

func TestObjectStoreResetBreaker_ThawsAFrozenCeiling(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()
	ceiling := freezeBucket(t, objectguard.CeilingLimit{MaxObjects: 2}, 10, 9)

	resp, err := http.Post(base+"/api/v1/object-store/reset-breaker", "application/json", nil)
	if err != nil {
		t.Fatalf("post reset-breaker: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Thawed  bool `json:"thawed"`
		Ceiling struct {
			Frozen bool `json:"frozen"`
		} `json:"ceiling"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Thawed {
		t.Error("resetting a frozen bucket does not report the thaw")
	}
	if body.Ceiling.Frozen {
		t.Error("the response to a reset still reports the bucket as frozen")
	}
	if ceiling.Frozen() {
		t.Error("the reset left the bucket frozen")
	}

	ceiling.Observe(objectguard.Usage{Bytes: 10, Objects: 9})
	if !ceiling.Frozen() {
		t.Error("the measurement after a thaw left a bucket over its ceiling writable")
	}
}

func TestObjectStoreHealth_ReportsAFrozenCeilingWithoutItsTotals(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()
	freezeBucket(t, objectguard.CeilingLimit{MaxBytes: 1000}, 999999, 7)

	resp := mustGet(t, base+"/api/v1/health")
	defer resp.Body.Close()
	var body struct {
		Status      string         `json:"status"`
		ObjectStore map[string]any `json:"object_store"`
		Problems    []string       `json:"problems"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ceiling, ok := body.ObjectStore["ceiling"].(map[string]any)
	if !ok {
		t.Fatalf("health carries no ceiling summary: %v", body.ObjectStore)
	}
	if ceiling["frozen"] != true {
		t.Errorf("health reports frozen=%v on a bucket over its ceiling", ceiling["frozen"])
	}
	if _, leaked := ceiling["bytes"]; leaked {
		t.Error("the unauthenticated health route carries the bucket's stored bytes")
	}
	if body.Status != "degraded" {
		t.Errorf("health status = %q while writes are frozen, want degraded", body.Status)
	}
	var named bool
	for _, p := range body.Problems {
		if strings.Contains(p, "bucket ceiling") {
			named = true
		}
	}
	if !named {
		t.Errorf("health problems do not name the frozen bucket: %v", body.Problems)
	}
}

func TestMetrics_BucketCeilingIsExported(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()
	freezeBucket(t, objectguard.CeilingLimit{MaxBytes: 1000, MaxObjects: 50}, 4096, 12)

	body := scrape(t, base)
	for _, want := range []string{
		"sparkwing_object_store_bucket_bytes 4096",
		"sparkwing_object_store_bucket_objects 12",
		`sparkwing_object_store_bucket_ceiling{unit="bytes"} 1000`,
		`sparkwing_object_store_bucket_ceiling{unit="objects"} 50`,
		"sparkwing_object_store_bucket_ceiling_frozen 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics do not carry %q", want)
		}
	}
}
