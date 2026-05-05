package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
