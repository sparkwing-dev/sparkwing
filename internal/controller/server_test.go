package controller_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestController_Health(t *testing.T) {
	f := newCronsFixture(t)
	var body map[string]any
	f.call(http.MethodGet, "/api/v1/health", "", nil, http.StatusOK, &body)
	if body["status"] != "ok" {
		t.Errorf("status=%q want ok", body["status"])
	}
	objectStore, ok := body["object_store"].(map[string]any)
	if !ok || objectStore["tripped"] != false {
		t.Fatalf("fresh controller object_store=%v", body["object_store"])
	}
}

func TestController_HealthHidesDatabaseErrors(t *testing.T) {
	f := newCronsFixture(t)
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Status   string   `json:"status"`
		Problems []string `json:"problems"`
	}
	f.call(http.MethodGet, "/api/v1/health", "", nil, http.StatusServiceUnavailable, &body)
	if body.Status != "degraded" || len(body.Problems) != 1 || body.Problems[0] != "db: unavailable" {
		t.Fatalf("public health exposed database failure details: %+v", body)
	}
}

func TestController_HealthDoesNotExposeRunFailures(t *testing.T) {
	f := newCronsFixture(t)
	for i := range 20 {
		if err := teststore.SeedRun(f.store, t.Context(), store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: fmt.Sprintf("failed-%d", i), Pipeline: "demo", Status: "failed", StartedAt: time.Now()}}); err != nil {
			t.Fatal(err)
		}
	}
	var body struct {
		Status           string   `json:"status"`
		Problems         []string `json:"problems"`
		RecentRunWarning string   `json:"recent_run_warning"`
	}
	f.call(http.MethodGet, "/api/v1/health", "", nil, http.StatusOK, &body)
	if body.Status != "ok" || len(body.Problems) != 0 || body.RecentRunWarning != "" {
		t.Fatalf("public health exposed workload history: %+v", body)
	}
}

func TestController_ConcurrencyStateOmitsQueueArrivedAtForImmediateHolder(t *testing.T) {
	checkQueueArrival(t, 0)
}

func TestController_ConcurrencyStateIncludesQueueArrivedAtForPromotedHolder(t *testing.T) {
	checkQueueArrival(t, time.Now().UnixNano())
}

type queueGroupReaderFake struct {
	controller.ConcurrencyStore
	tenant *store.Tenant
	seen   capability.Authorized
	calls  int
}

func (f *queueGroupReaderFake) Team() store.Team { return f.tenant.Team() }

func (f *queueGroupReaderFake) ReadAuthorizedGroup(ctx context.Context, a capability.Authorized, key string) (*store.ConcurrencyState, error) {
	f.seen = a
	f.calls++
	return f.tenant.GetConcurrencyState(ctx, key)
}

func checkQueueArrival(t *testing.T, arrival int64) {
	t.Helper()
	f := newCronsFixture(t)
	if err := teststore.SeedRun(f.store, t.Context(), store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "run", Pipeline: "ci", Status: "running", StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedNode(f.store, t.Context(), store.Node{RunID: "run", NodeID: "job", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	seedControllerCapacityHolder(t, f.store, store.DefaultTeam, "slot", "holder", "run", "job", arrival)
	var body struct {
		Holders []map[string]json.RawMessage `json:"holders"`
	}
	reader := &queueGroupReaderFake{tenant: teststore.ForTeam(t, f.store, store.DefaultTeam)}
	f.handler = controller.BindTenantPortForTest[controller.ConcurrencyStore](controller.New(f.store, nil), func(_ context.Context, team store.Team) (controller.ConcurrencyStore, error) {
		if team != reader.tenant.Team() {
			t.Fatalf("resolved foreign tenant %s", team)
		}
		return reader, nil
	}).Handler()
	f.call(http.MethodGet, "/api/v1/queue/groups/slot", f.reader, nil, http.StatusOK, &body)
	if reader.calls != 1 || reader.seen.Target.Team != string(store.DefaultTeam) || reader.seen.Target.SlotKey != "slot" || reader.seen.Principal.Capability.Subject.ID != "viewer" {
		t.Fatalf("queue reader calls=%d authority=%+v", reader.calls, reader.seen)
	}
	if len(body.Holders) != 1 {
		t.Fatalf("holders=%d want 1", len(body.Holders))
	}
	got, present := body.Holders[0]["queue_arrived_at"]
	if present != (arrival != 0) {
		t.Fatalf("queue_arrived_at=%s present=%v, want %v", got, present, arrival != 0)
	}
	if present {
		var decoded time.Time
		if err := json.Unmarshal(got, &decoded); err != nil || !decoded.Equal(time.Unix(0, arrival)) {
			t.Fatalf("queue_arrived_at=%s want %v: %v", got, time.Unix(0, arrival), err)
		}
	}
}

func TestController_GetRunIncludesJobs(t *testing.T) {
	f := newCronsFixture(t)
	seedReportRun(t, f, "run-incl", []string{"a", "b"})
	var first controllerwire.RunDetail
	f.call(http.MethodGet, "/api/v1/runs/run-incl", f.reader, nil, http.StatusOK, &first)
	if first.ID != "run-incl" || first.Status != "running" || first.Team != string(store.DefaultTeam) || len(first.Jobs) != 2 || first.Jobs[0].ID != "a" || first.Jobs[1].ID != "b" {
		t.Fatalf("run detail=%+v", first)
	}
	var second controllerwire.RunDetail
	f.call(http.MethodGet, "/api/v1/runs/run-incl?include=nodes,secret_values", f.reader, nil, http.StatusOK, &second)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("legacy query changed public status: %+v -> %+v", first, second)
	}
	var raw map[string]json.RawMessage
	f.call(http.MethodGet, "/api/v1/runs/run-incl", f.reader, nil, http.StatusOK, &raw)
	for _, retired := range []string{"run", "nodes", "plan_snapshot"} {
		if _, exists := raw[retired]; exists {
			t.Fatalf("public status contains %q: %s", retired, raw[retired])
		}
	}
}

func TestController_GetRunBeforePlanning(t *testing.T) {
	f := newCronsFixture(t)
	seedReportRun(t, f, "run-bare", []string{"a"})
	var detail controllerwire.RunDetail
	f.call(http.MethodGet, "/api/v1/runs/run-bare", f.reader, nil, http.StatusOK, &detail)
	if detail.Engine != nil || len(detail.Jobs) != 1 || detail.Jobs[0].ID != "a" || detail.Attempts == nil || detail.Logs == nil || detail.Notices == nil {
		t.Fatalf("unplanned detail=%+v", detail)
	}
	var raw struct {
		Jobs []map[string]json.RawMessage `json:"jobs"`
	}
	f.call(http.MethodGet, "/api/v1/runs/run-bare", f.reader, nil, http.StatusOK, &raw)
	for _, job := range raw.Jobs {
		if _, exists := job["decorations"]; exists {
			t.Fatal("unplanned job exposes raw decorations")
		}
	}
}

func seedReportRun(t *testing.T, f *cronsFixture, id string, jobs []string) {
	t.Helper()
	if err := teststore.SeedRun(f.store, t.Context(), store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: id, Pipeline: "ci", Status: "running", StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if err := teststore.SeedNode(f.store, t.Context(), store.Node{RunID: id, NodeID: job, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestControllerPublicHealthHidesWorkloadProperty(t *testing.T) {
	property := func(value []byte, failed bool) bool {
		f := newCronsFixture(t)
		secret := "private-workload-" + hex.EncodeToString(value)
		status := "running"
		if failed {
			status = "failed"
		}
		if err := teststore.SeedRun(f.store, t.Context(), store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "private-run", Pipeline: secret, Status: status, Error: secret, Args: map[string]string{"secret": secret}, StartedAt: time.Now()}}); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		var body struct {
			Status string `json:"status"`
		}
		return w.Code == http.StatusOK && json.Unmarshal(w.Body.Bytes(), &body) == nil && body.Status == "ok" && !strings.Contains(w.Body.String(), secret) && !strings.Contains(w.Body.String(), "private-run")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 20}); err != nil {
		t.Fatal(err)
	}
}

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func seedControllerCapacityHolder(t *testing.T, s *store.Store, team store.Team, key, holder, run, node string, queueArrival int64) {
	t.Helper()
	now := time.Now()
	if _, err := teststore.SQL(t, s).ExecContext(t.Context(), `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ($1,$2,1,$3) ON CONFLICT(team,key) DO NOTHING`, team, key, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, s).ExecContext(t.Context(), `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,queue_arrived_at,lease_expires_at,cost,declared_capacity) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1,1)`, team, key, holder, run, node, now.UnixNano(), queueArrival, now.Add(time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
}
