package controller_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// newAuthedTestServer spins up a Server with EnableAuthFromStore
// wired against a tokens table seeded with one admin row. Used by
// tests that pin the "/metrics stays unauthenticated even when auth
// is enabled" guarantee.
func newAuthedTestServer(t *testing.T) (baseURL string, st *store.Store, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Seed a token row so EnableAuthFromStore actually turns auth on
	// (empty tokens table = pass-through).
	if _, _, err := s.CreateToken("test-admin", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, time.Now().UTC()); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	ctrl := controller.New(s, nil).EnableAuthFromStore()
	srv := httptest.NewServer(ctrl.Handler())
	return srv.URL, s, func() {
		srv.Close()
		_ = s.Close()
	}
}

// TestMetrics_EndpointReachable checks that /metrics returns 200, is
// the sparkwing custom registry (not just the default), and exposes
// all six session-1 collectors plus the Go runtime metrics.
func TestMetrics_EndpointReachable(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	resp := mustGet(t, base+"/metrics")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status=%d want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	out := string(body)

	// Gauges + runtime collectors always emit, even with no
	// observations. Counter and histogram vecs only emit after the
	// first observation -- those are covered by the activity test
	// below.
	mustContain := []string{
		"sparkwing_pending_nodes",
		"sparkwing_active_runners",
		"go_goroutines",
		"process_resident_memory_bytes",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}

// TestMetrics_RunsCounterIncrements drives a CreateRun + FinishRun
// cycle and asserts the counter row appears on the next scrape. Uses
// a unique pipeline label so parallel test runs don't interfere.
func TestMetrics_RunsCounterIncrements(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	const pipeline = "prom-test-pipeline-runs"
	run := store.Run{
		ID:        "run-prom-runs-1",
		Pipeline:  pipeline,
		Status:    "running",
		StartedAt: time.Now().Add(-3 * time.Second),
	}
	mustPostJSON(t, base+"/api/v1/runs", run, http.StatusCreated)
	mustPostJSON(t, base+"/api/v1/runs/run-prom-runs-1/finish",
		map[string]any{"status": "success"},
		http.StatusNoContent)

	body := scrape(t, base)
	// Counter: exactly one terminal event for this pipeline.
	wantCounter := `sparkwing_runs_total{pipeline="` + pipeline + `",status="success"} 1`
	if !strings.Contains(body, wantCounter) {
		t.Errorf("/metrics missing or wrong counter row\nwant substring: %s\ngot:\n%s", wantCounter, body)
	}
	// Histogram: at least one observation for this pipeline+outcome.
	wantHist := `sparkwing_run_duration_seconds_count{outcome="success",pipeline="` + pipeline + `"}`
	if !strings.Contains(body, wantHist) {
		t.Errorf("/metrics missing histogram count row %q:\n%s", wantHist, body)
	}
}

// TestMetrics_CardinalityGuard verifies the scrape output never
// includes values that would blow up per-node or per-principal
// cardinality. Guard regexes reject a `node_id=` label, a raw token
// prefix (`sw?_`), or a run-id-looking label value.
func TestMetrics_CardinalityGuard(t *testing.T) {
	base, st, cleanup := newTestServer(t)
	defer cleanup()

	// Seed a run + finish so at least one sparkwing_* row is present.
	run := store.Run{
		ID:        "run-card-1",
		Pipeline:  "card-check",
		Status:    "running",
		StartedAt: time.Now(),
	}
	if err := st.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := st.FinishRun(context.Background(), run.ID, "success", ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	mustPostJSON(t, base+"/api/v1/runs/run-card-1/finish",
		map[string]any{"status": "success"},
		http.StatusNoContent)

	body := scrape(t, base)

	// Only inspect sparkwing_* rows -- go_* and process_* rows are
	// opaque runtime metrics we don't label.
	banned := []*regexp.Regexp{
		regexp.MustCompile(`sparkwing_[a-z_]+\{[^}]*\bnode_id="`),
		regexp.MustCompile(`sparkwing_[a-z_]+\{[^}]*\bprincipal="`),
		regexp.MustCompile(`sparkwing_[a-z_]+\{[^}]*\bholder_id="`),
		regexp.MustCompile(`sparkwing_[a-z_]+\{[^}]*="sw[urs]_`),
		regexp.MustCompile(`sparkwing_[a-z_]+\{[^}]*="run-card-1"`),
	}
	for _, rx := range banned {
		if rx.MatchString(body) {
			t.Errorf("cardinality guard hit: pattern %q matched /metrics output", rx.String())
		}
	}
}

func scrape(t *testing.T, base string) string {
	t.Helper()
	resp := mustGet(t, base+"/metrics")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	return string(body)
}

// TestMetrics_HTTPRequestInstrumentation verifies every served
// request flows into sparkwing_http_requests_total +
// sparkwing_http_request_duration_seconds under a normalized route
// pattern (never the raw URL path). Guards against both "no data"
// regressions and cardinality blowups from ids in labels.
func TestMetrics_HTTPRequestInstrumentation(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	// Health is unauthenticated and always reachable.
	resp := mustGet(t, base+"/api/v1/health")
	resp.Body.Close()

	body := scrape(t, base)

	// Counter row for the health route exists with a normalized path,
	// a method, and a 200 status.
	wantCounter := `sparkwing_http_requests_total{method="GET",route="/api/v1/health",status="200"}`
	if !strings.Contains(body, wantCounter) {
		t.Errorf("/metrics missing http counter row %q:\n%s", wantCounter, body)
	}
	// Histogram _count row is present for the same method+route.
	wantHist := `sparkwing_http_request_duration_seconds_count{method="GET",route="/api/v1/health"}`
	if !strings.Contains(body, wantHist) {
		t.Errorf("/metrics missing http duration histogram %q:\n%s", wantHist, body)
	}
}

// TestMetrics_HTTPRouteNormalization confirms variable URL segments
// (run ids, node ids) collapse into the registered route pattern so
// per-id cardinality can't leak into label values.
func TestMetrics_HTTPRouteNormalization(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	// GETs against non-existent runs are fine -- 404 still records.
	for _, id := range []string{"abc", "def", "xyz-123"} {
		resp := mustGet(t, base+"/api/v1/runs/"+id)
		resp.Body.Close()
	}

	body := scrape(t, base)

	// Only one normalized row, not three per-id rows.
	wantRoute := `route="/api/v1/runs/{id}"`
	if !strings.Contains(body, wantRoute) {
		t.Errorf("/metrics missing normalized run route label %q:\n%s", wantRoute, body)
	}
	// Assert the raw ids never enter a label value.
	for _, id := range []string{"abc", "def", "xyz-123"} {
		if strings.Contains(body, `route="/api/v1/runs/`+id) {
			t.Errorf("raw run id %q leaked into route label", id)
		}
	}
}

// TestMetrics_EndpointUnauthWithAuthEnabled pins down the
// FOLLOWUPS #2 + #5 guarantee: even when the authenticator is on,
// Prometheus can still scrape /metrics without an Authorization
// header. Regression guard for routing order in Server.Handler().
func TestMetrics_EndpointUnauthWithAuthEnabled(t *testing.T) {
	base, st, cleanup := newAuthedTestServer(t)
	defer cleanup()
	_ = st

	// No Authorization header on the scrape.
	resp := mustGet(t, base+"/metrics")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics under auth expected 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "sparkwing_pending_nodes") {
		t.Errorf("/metrics under auth returned 200 but body is not the sparkwing registry:\n%s", string(body))
	}

	// Sanity: an authed endpoint without a header still 401s. Proves
	// auth really is live; /metrics is routed around it.
	resp2 := mustGet(t, base+"/api/v1/runs")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/v1/runs without auth: expected 401, got %d", resp2.StatusCode)
	}
}
