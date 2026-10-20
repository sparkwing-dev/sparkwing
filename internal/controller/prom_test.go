package controller_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func newAuthedTestServer(t *testing.T) (baseURL, admin string, st *store.Store, cleanup func()) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	srv := httptest.NewServer(f.h)
	return srv.URL, f.token, f.st, srv.Close
}

func getWithBearer(t *testing.T, url, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func TestMetrics_EndpointReachable(t *testing.T) {
	h := newMetricsHTTPHandler(t, capability.OperatorLimits)
	out := metricsHTTPResponse(t, h, http.MethodGet, "/metrics", "operator").Body.String()
	for _, want := range []string{"sparkwing_pending_nodes", "sparkwing_active_runners", "sparkwing_object_store_requests_total", "sparkwing_object_store_trips_total", "sparkwing_object_store_tripped", "go_goroutines", "process_resident_memory_bytes"} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}

func TestMetrics_CardinalityGuard(t *testing.T) {
	h := newMetricsHTTPHandler(t, capability.OperatorLimits)
	metricsHTTPResponse(t, h, http.MethodGet, "/api/v1/runs/run-card-1", "operator")
	body := metricsHTTPResponse(t, h, http.MethodGet, "/metrics", "operator").Body.String()
	for _, pattern := range []string{
		`sparkwing_[a-z_]+\{[^}]*\bnode_id="`,
		`sparkwing_[a-z_]+\{[^}]*\bprincipal="`,
		`sparkwing_[a-z_]+\{[^}]*\bholder_id="`,
		`sparkwing_[a-z_]+\{[^}]*="sw[ursk]_`,
		`sparkwing_[a-z_]+\{[^}]*="run-card-1"`,
	} {
		if regexp.MustCompile(pattern).MatchString(body) {
			t.Errorf("cardinality guard hit: pattern %q matched /metrics output", pattern)
		}
	}
}

func TestMetrics_HTTPRequestInstrumentation(t *testing.T) {
	h := newMetricsHTTPHandler(t, capability.OperatorLimits)
	metricsHTTPResponse(t, h, http.MethodGet, "/api/v1/health", "")
	body := metricsHTTPResponse(t, h, http.MethodGet, "/metrics", "operator").Body.String()
	for _, want := range []string{
		`sparkwing_http_requests_total{method="GET",route="/api/v1/health",status="200"}`,
		`sparkwing_http_request_duration_seconds_count{method="GET",route="/api/v1/health"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}

func TestMetrics_HTTPRouteNormalization(t *testing.T) {
	h := newMetricsHTTPHandler(t, capability.OperatorLimits)
	for _, id := range []string{"abc", "def", "xyz-123"} {
		metricsHTTPResponse(t, h, http.MethodGet, "/api/v1/runs/"+id, "operator")
	}
	body := metricsHTTPResponse(t, h, http.MethodGet, "/metrics", "operator").Body.String()
	if !strings.Contains(body, `route="/api/v1/runs/{run}"`) {
		t.Error("/metrics missing normalized run route")
	}
	for _, id := range []string{"abc", "def", "xyz-123"} {
		if strings.Contains(body, `route="/api/v1/runs/`+id) {
			t.Errorf("raw run id %q leaked into route label", id)
		}
	}
}

func TestMetrics_RequiresAdminWithAuthEnabled(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		grant       capability.Grant
		want        int
	}{
		{"anonymous", "", capability.OperatorLimits, http.StatusUnauthorized},
		{"read grant", "operator", capability.RunsRead, http.StatusForbidden},
		{"operator", "operator", capability.OperatorLimits, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMetricsHTTPHandler(t, tc.grant)
			response := metricsHTTPResponse(t, h, http.MethodGet, "/metrics", tc.token)
			if response.Code != tc.want || strings.Contains(response.Body.String(), "sparkwing_pending_nodes") != (tc.want == http.StatusOK) {
				t.Fatalf("metrics status=%d body=%s, want %d", response.Code, response.Body, tc.want)
			}
		})
	}
}

func TestMetricsAddr_MovesMetricsOffTheAPIListener(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	metricsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = metricsLn.Close() })
	metricsAddr := metricsLn.Addr().String()
	var order []string
	principal := capability.Principal{Class: capability.CredentialBearer, Capability: capability.Capability{
		Audience: capability.Controller, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator"},
		Grants: []capability.Grant{capability.OperatorLimits}, Binding: capability.Binding{Kind: capability.BindingNone},
	}}
	srv := controller.New(st, nil).WithEngineController(controller.NewEngineController(st, controller.EngineController{RunnerPolicy: &controller.SignedRunnerAdmission{}})).WithMetricsAddr(metricsAddr).WithAuthorization(controller.RouteAuthorization{
		Authenticator: policyAuth{principal: principal, order: &order}, Bindings: policyBindings{order: &order},
	})

	api := httptest.NewServer(srv.Handler())
	t.Cleanup(api.Close)
	resp := mustGet(t, api.URL+"/metrics")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/metrics on the API listener status = %d, want 404", resp.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	listeners := controller.Listeners{API: apiListener(t), Metrics: metricsLn}
	go func() { served <- controller.ServeWith(ctx, srv, listeners) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("ServeWith: %v", err)
		}
	})

	// safety: the listener is bound before ServeWith runs, so this request
	// waits in its backlog until the metrics server starts answering.
	answered, stopWaiting := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopWaiting()
	req, rerr := http.NewRequestWithContext(answered, http.MethodGet, "http://"+metricsAddr+"/metrics", nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	req.Header.Set("Authorization", "Bearer operator")
	metrics, gerr := http.DefaultClient.Do(req)
	if gerr != nil {
		t.Fatalf("metrics listener never answered: %v", gerr)
	}
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
}

func TestMetricsAddr_FailsStartupWithoutAMetricsListener(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := controller.New(st, nil).WithEngineController(controller.NewEngineController(st, controller.EngineController{RunnerPolicy: &controller.SignedRunnerAdmission{}})).WithMetricsAddr("127.0.0.1:9100")
	if err := controller.ServeWith(ctx, srv, controller.Listeners{API: apiListener(t)}); err == nil || !strings.Contains(err.Error(), "127.0.0.1:9100") {
		t.Fatalf("ServeWith error = %v, want the missing metrics listener", err)
	}
}

func apiListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func TestServeWithRefusesAServerWithoutRunnerPolicy(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := controller.ServeWith(ctx, controller.New(st, nil), controller.Listeners{API: apiListener(t)}); err == nil || !strings.Contains(err.Error(), "runner admission policy") {
		t.Fatalf("ServeWith error = %v, want a startup refusal", err)
	}
}

func TestMetrics_HTTPRouteCollapsesEveryPathParameter(t *testing.T) {
	h := newMetricsHTTPHandler(t, capability.OperatorLimits)
	for _, path := range []string{
		"/api/v1/queue/groups/prom-key-alpha",
		"/api/v1/queue/groups/prom-key-beta",
		"/api/v1/queue/groups/prom-key-gamma",
		"/api/v1/runs/prom-run-2/approvals/prom-node-2",
		"/api/v1/runs/prom-run-1/approvals/prom-node-1",
		"/api/v1/prom-unrouted-path",
	} {
		metricsHTTPResponse(t, h, http.MethodGet, path, "operator")
	}
	body := metricsHTTPResponse(t, h, http.MethodGet, "/metrics", "operator").Body.String()
	for _, want := range []string{`route="/api/v1/queue/groups/{group}"`, `route="/api/v1/runs/{run}/approvals/{job}"`, `route="other"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing collapsed route label %q", want)
		}
	}
	for _, leaked := range []string{"prom-key-alpha", "prom-key-beta", "prom-key-gamma", "prom-run-2", "prom-node-2", "prom-run-1", "prom-node-1", "prom-unrouted-path"} {
		if strings.Contains(body, leaked) {
			t.Errorf("caller-supplied segment %q leaked into a metric label", leaked)
		}
	}
}

func TestMetrics_HTTPMethodLabelClampsInventedMethods(t *testing.T) {
	h := newMetricsHTTPHandler(t, capability.OperatorLimits)
	for _, method := range []string{"PROMMETHODA", "PROMMETHODB", "PROMMETHODC"} {
		metricsHTTPResponse(t, h, method, "/api/v1/runs", "operator")
	}
	body := metricsHTTPResponse(t, h, http.MethodGet, "/metrics", "operator").Body.String()
	if !strings.Contains(body, `method="other"`) {
		t.Error("/metrics missing the clamped method label")
	}
	for _, method := range []string{"PROMMETHODA", "PROMMETHODB", "PROMMETHODC"} {
		if strings.Contains(body, method) {
			t.Errorf("invented method %q leaked into a metric label", method)
		}
	}
}

func newMetricsHTTPHandler(t *testing.T, grant capability.Grant) http.Handler {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var order []string
	principal := capability.Principal{Class: capability.CredentialBearer, Capability: capability.Capability{
		Audience: capability.Controller, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator"},
		Grants: []capability.Grant{grant}, Binding: capability.Binding{Kind: capability.BindingNone},
	}}
	return controller.New(st, nil).WithAuthorization(controller.RouteAuthorization{
		Authenticator: policyAuth{principal: principal, order: &order}, Bindings: policyBindings{order: &order},
	}).Handler()
}

func metricsHTTPResponse(t *testing.T, h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
