package controller_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newAuthedTestServer(t *testing.T) (baseURL string, st *store.Store, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
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

func TestMetrics_RunsCounterIncrements(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	const pipeline = "prom-test-pipeline-runs"
	counterPrefix := `sparkwing_runs_total{pipeline="` + pipeline + `",status="success"}`
	histogramPrefix := `sparkwing_run_duration_seconds_count{outcome="success",pipeline="` + pipeline + `"}`
	before := scrape(t, base)
	beforeCounter := metricSampleValue(t, before, counterPrefix)
	beforeHistogram := metricSampleValue(t, before, histogramPrefix)

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

	after := scrape(t, base)
	if got := metricSampleValue(t, after, counterPrefix); got != beforeCounter+1 {
		t.Errorf("runs counter=%v before=%v, want one increment", got, beforeCounter)
	}
	if got := metricSampleValue(t, after, histogramPrefix); got != beforeHistogram+1 {
		t.Errorf("run duration count=%v before=%v, want one observation", got, beforeHistogram)
	}
}

func metricSampleValue(t *testing.T, body, prefix string) float64 {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		value, ok := strings.CutPrefix(line, prefix+" ")
		if !ok {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			t.Fatalf("parse metric sample %q: %v", line, err)
		}
		return parsed
	}
	return 0
}

func TestMetrics_CardinalityGuard(t *testing.T) {
	base, st, cleanup := newTestServer(t)
	defer cleanup()

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

func TestMetrics_HTTPRequestInstrumentation(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	resp := mustGet(t, base+"/api/v1/health")
	resp.Body.Close()

	body := scrape(t, base)

	wantCounter := `sparkwing_http_requests_total{method="GET",route="/api/v1/health",status="200"}`
	if !strings.Contains(body, wantCounter) {
		t.Errorf("/metrics missing http counter row %q:\n%s", wantCounter, body)
	}
	wantHist := `sparkwing_http_request_duration_seconds_count{method="GET",route="/api/v1/health"}`
	if !strings.Contains(body, wantHist) {
		t.Errorf("/metrics missing http duration histogram %q:\n%s", wantHist, body)
	}
}

func TestMetrics_HTTPRouteNormalization(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	for _, id := range []string{"abc", "def", "xyz-123"} {
		resp := mustGet(t, base+"/api/v1/runs/"+id)
		resp.Body.Close()
	}

	body := scrape(t, base)

	wantRoute := `route="/api/v1/runs/{id}"`
	if !strings.Contains(body, wantRoute) {
		t.Errorf("/metrics missing normalized run route label %q:\n%s", wantRoute, body)
	}
	for _, id := range []string{"abc", "def", "xyz-123"} {
		if strings.Contains(body, `route="/api/v1/runs/`+id) {
			t.Errorf("raw run id %q leaked into route label", id)
		}
	}
}

func TestMetrics_EndpointUnauthWithAuthEnabled(t *testing.T) {
	base, st, cleanup := newAuthedTestServer(t)
	defer cleanup()
	_ = st

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

	resp2 := mustGet(t, base+"/api/v1/runs")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/v1/runs without auth: expected 401, got %d", resp2.StatusCode)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}
	return addr
}

func TestMetricsAddr_MovesMetricsOffTheAPIListener(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	metricsAddr := freeAddr(t)
	apiAddr := freeAddr(t)
	srv := controller.New(st, nil).WithMetricsAddr(metricsAddr)

	api := httptest.NewServer(srv.Handler())
	t.Cleanup(api.Close)
	resp := mustGet(t, api.URL+"/metrics")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/metrics on the API listener status = %d, want 404", resp.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- controller.ServeWith(ctx, srv, apiAddr) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("ServeWith: %v", err)
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		metrics, gerr := http.Get("http://" + metricsAddr + "/metrics")
		if gerr == nil {
			body, rerr := io.ReadAll(metrics.Body)
			_ = metrics.Body.Close()
			if rerr != nil {
				t.Fatalf("read metrics: %v", rerr)
			}
			if metrics.StatusCode != http.StatusOK {
				t.Fatalf("metrics listener status = %d, want 200", metrics.StatusCode)
			}
			if !strings.Contains(string(body), "go_goroutines") {
				t.Fatalf("metrics body missing go_goroutines: %s", body)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics listener never answered: %v", gerr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMetricsAddr_FailsStartupWhenTheMetricsPortIsTaken(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := controller.New(st, nil).WithMetricsAddr(held.Addr().String())
	if err := controller.ServeWith(ctx, srv, freeAddr(t)); err == nil {
		t.Fatal("ServeWith error = nil, want the bind failure")
	}
}

func TestMetrics_HTTPRouteCollapsesEveryPathParameter(t *testing.T) {
	base := newServerWithArtifacts(t, &fakeArtifactStore{})

	for _, path := range []string{
		"/api/v1/concurrency/prom-key-alpha/state",
		"/api/v1/concurrency/prom-key-beta/state",
		"/api/v1/concurrency/prom-key-gamma/state",
		"/api/v1/artifacts/prom-digest-aaa",
		"/api/v1/artifacts/prom-digest-bbb",
		"/api/v1/runs/prom-run-1/approvals/prom-node-1",
		"/api/v1/prom-unrouted-path",
	} {
		resp := mustGet(t, base+path)
		resp.Body.Close()
	}

	body := scrape(t, base)

	for _, want := range []string{
		`route="/api/v1/concurrency/{key}/state"`,
		`route="/api/v1/artifacts/{key}"`,
		`route="/api/v1/runs/{id}/approvals/{nodeID}"`,
		`route="other"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing collapsed route label %q", want)
		}
	}
	for _, leaked := range []string{
		"prom-key-alpha", "prom-key-beta", "prom-key-gamma",
		"prom-digest-aaa", "prom-digest-bbb",
		"prom-run-1", "prom-node-1", "prom-unrouted-path",
	} {
		if strings.Contains(body, leaked) {
			t.Errorf("caller-supplied segment %q leaked into a metric label", leaked)
		}
	}
}
