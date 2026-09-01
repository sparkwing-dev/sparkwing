package web

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/health"
)

func TestHealthServices_AllOK(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	services := []HealthService{
		{Name: "controller", URL: upstream.URL + "/api/v1/health"},
		{Name: "logs", URL: upstream.URL + "/api/v1/health"},
	}
	h := healthServicesHandler(services, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health/services", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var body struct {
		Services []serviceStatus `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Services) != 2 {
		t.Fatalf("services=%d want 2", len(body.Services))
	}
	for _, svc := range body.Services {
		if svc.Status != "ok" {
			t.Errorf("%s status=%s want ok", svc.Name, svc.Status)
		}
	}
}

func TestHealthServices_DownServiceReported(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer up.Close()

	services := []HealthService{
		{Name: "sick", URL: up.URL + "/health"},
	}
	rec := httptest.NewRecorder()
	healthServicesHandler(services, "")(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var body struct {
		Services []serviceStatus `json:"services"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Services) != 1 || body.Services[0].Status != "down" {
		t.Fatalf("expected down status, got %+v", body.Services)
	}
}

func TestHealthServices_TokenAttached(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	services := []HealthService{{Name: "logs", URL: up.URL}}
	rec := httptest.NewRecorder()
	healthServicesHandler(services, "s3cr3t")(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if gotAuth != "Bearer s3cr3t" {
		t.Fatalf("Authorization header=%q want Bearer s3cr3t", gotAuth)
	}
}

func TestHealthServices_Empty(t *testing.T) {
	rec := httptest.NewRecorder()
	healthServicesHandler(nil, "")(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var body struct {
		Services []serviceStatus `json:"services"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Services) != 0 {
		t.Fatalf("expected empty services, got %d", len(body.Services))
	}
}

func TestProbeService_DegradedBodyAtHTTP200(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantStatus   string
		wantProblems []string
	}{
		{
			name:       "controller healthy",
			body:       `{"status":"ok","auth":"enabled"}`,
			wantStatus: "ok",
		},
		{
			name: "controller degraded",
			body: `{"status":"degraded","auth":"enabled","problems":` +
				`["triggers: 3 claimed >30m without /done","runs: 61% success over 44 (24h), 17 failed"]}`,
			wantStatus: "degraded",
			wantProblems: []string{
				"triggers: 3 claimed >30m without /done",
				"runs: 61% success over 44 (24h), 17 failed",
			},
		},
		{
			name:         "logs degraded on low disk",
			body:         `{"status":"degraded","problems":["root: disk free 412MiB (<1GiB) on /data"]}`,
			wantStatus:   "degraded",
			wantProblems: []string{"root: disk free 412MiB (<1GiB) on /data"},
		},
		{
			name: "cache degraded on stalled fetch",
			body: `{"status":"degraded","problems":["gitcache: background fetch failing for all 7 repos",` +
				`"proxy: cache directory not writable: permission denied"]}`,
			wantStatus: "degraded",
			wantProblems: []string{
				"gitcache: background fetch failing for all 7 repos",
				"proxy: cache directory not writable: permission denied",
			},
		},
		{
			name:         "degraded without problems still names itself",
			body:         `{"status":"degraded"}`,
			wantStatus:   "degraded",
			wantProblems: []string{"service reports degraded"},
		},
		{
			name:       "non-JSON body stays ok",
			body:       "OK\n",
			wantStatus: "ok",
		},
		{
			name:       "empty body stays ok",
			body:       "",
			wantStatus: "ok",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer up.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			got := probeService(ctx, HealthService{Name: "svc", URL: up.URL}, "")

			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (problems %v)", got.Status, tc.wantStatus, got.Problems)
			}
			if len(got.Problems) != len(tc.wantProblems) {
				t.Fatalf("problems = %v, want %v", got.Problems, tc.wantProblems)
			}
			for i, want := range tc.wantProblems {
				if got.Problems[i] != want {
					t.Errorf("problems[%d] = %q, want %q", i, got.Problems[i], want)
				}
			}
		})
	}
}

func TestHealthServices_DegradedBodyReachesTheResponse(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"degraded","problems":["proxy: cache directory not writable"]}`)
	}))
	defer up.Close()

	rec := httptest.NewRecorder()
	healthServicesHandler([]HealthService{{Name: "cache", URL: up.URL + "/health"}}, "")(
		rec, httptest.NewRequest(http.MethodGet, "/", nil))

	var body struct {
		Services []serviceStatus `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Services) != 1 {
		t.Fatalf("services = %d, want 1", len(body.Services))
	}
	svc := body.Services[0]
	if svc.Status != "degraded" {
		t.Fatalf("status = %q, want degraded", svc.Status)
	}
	if len(svc.Problems) != 1 || svc.Problems[0] != "proxy: cache directory not writable" {
		t.Fatalf("problems = %v, want the upstream problem", svc.Problems)
	}
}

func TestDefaultServices_CacheIsProbedWhenConfigured(t *testing.T) {
	got := defaultServices(HandlerOptions{
		ControllerURL: "http://controller",
		CacheURL:      "http://cache",
	}, "http://logs/")

	want := []HealthService{
		{Name: "controller", URL: "http://controller/api/v1/health"},
		{Name: "logs", URL: "http://logs/api/v1/health"},
		{Name: "cache", URL: "http://cache/health"},
	}
	if len(got) != len(want) {
		t.Fatalf("services = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("services[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDefaultServices_CacheAbsentWhenUnconfigured(t *testing.T) {
	got := defaultServices(HandlerOptions{ControllerURL: "http://controller"}, "")
	if len(got) != 1 || got[0].Name != "controller" {
		t.Fatalf("services = %+v, want controller alone", got)
	}
}

func TestNoteSlowResponse_SurvivesADegradedBody(t *testing.T) {
	cases := []struct {
		name         string
		in           serviceStatus
		wantStatus   string
		wantProblems []string
	}{
		{
			name:       "fast and healthy",
			in:         serviceStatus{Status: "ok", LatencyMs: 40},
			wantStatus: "ok",
		},
		{
			name:         "slow and healthy",
			in:           serviceStatus{Status: "ok", LatencyMs: 2100},
			wantStatus:   "degraded",
			wantProblems: []string{"slow response: 2100ms"},
		},
		{
			name: "slow and already reporting problems",
			in: serviceStatus{
				Status:    "degraded",
				LatencyMs: 2100,
				Problems:  []string{"root: disk free 412MiB (<1GiB) on /data"},
			},
			wantStatus: "degraded",
			wantProblems: []string{
				"root: disk free 412MiB (<1GiB) on /data",
				"slow response: 2100ms",
			},
		},
		{
			name:         "slow behind an auth wall",
			in:           serviceStatus{Status: "degraded", LatencyMs: 2100, Error: "HTTP 401 (auth wall)"},
			wantStatus:   "degraded",
			wantProblems: []string{"slow response: 2100ms"},
		},
		{
			name:       "unreachable",
			in:         serviceStatus{Status: "down", LatencyMs: 3000, Error: "context deadline exceeded"},
			wantStatus: "down",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in
			noteSlowResponse(&got)
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if len(got.Problems) != len(tc.wantProblems) {
				t.Fatalf("problems = %v, want %v", got.Problems, tc.wantProblems)
			}
			for i, want := range tc.wantProblems {
				if got.Problems[i] != want {
					t.Errorf("problems[%d] = %q, want %q", i, got.Problems[i], want)
				}
			}
		})
	}
}

func TestProbeService_ReusesTheConnection(t *testing.T) {
	oversized := `{"status":"ok","problems":["` + strings.Repeat("x", health.MaxBodyBytes) + `"]}`
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "healthy", status: http.StatusOK, body: `{"status":"ok"}`},
		{name: "body past the read bound", status: http.StatusOK, body: oversized},
		{name: "auth wall", status: http.StatusUnauthorized, body: "forbidden\n"},
		{name: "server error", status: http.StatusInternalServerError, body: "boom\n"},
		{name: "not found", status: http.StatusNotFound, body: "no such route\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var conns int32
			up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			up.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					atomic.AddInt32(&conns, 1)
				}
			}
			up.Start()
			defer up.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			svc := HealthService{Name: "svc", URL: up.URL}
			for i := 0; i < 3; i++ {
				probeService(ctx, svc, "")
			}
			if got := atomic.LoadInt32(&conns); got != 1 {
				t.Fatalf("three probes opened %d connections, want 1 reused", got)
			}
		})
	}
}

func TestProbeService_DegradedOnAuthWall(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer up.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := probeService(ctx, HealthService{Name: "logs", URL: up.URL}, "")
	if s.Status != "degraded" {
		t.Fatalf("status=%s want degraded", s.Status)
	}
}
