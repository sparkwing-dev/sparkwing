package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestServicesFor_ReturnsAnnouncedDashboard(t *testing.T) {
	resetCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/services" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(services{Dashboard: "https://dashboard.example.dev"})
	}))
	defer srv.Close()

	svc, err := servicesFor(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatalf("ServicesFor: %v", err)
	}
	if svc.Dashboard != "https://dashboard.example.dev" {
		t.Errorf("Dashboard = %q", svc.Dashboard)
	}
}

func TestServicesFor_CachesAcrossCalls(t *testing.T) {
	resetCache()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(services{Dashboard: "https://x"})
	}))
	defer srv.Close()

	for range 5 {
		if _, err := servicesFor(context.Background(), srv.URL, "t"); err != nil {
			t.Fatalf("ServicesFor: %v", err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("controller hit %d times across 5 calls; want 1 (cached)", got)
	}
}

func TestServicesFor_404ReturnsNoServices(t *testing.T) {
	resetCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	svc, err := servicesFor(context.Background(), srv.URL, "")
	if err != nil {
		t.Errorf("404 should report no error (caller falls back); got %v", err)
	}
	if svc.Dashboard != "" {
		t.Errorf("Dashboard should be empty on 404; got %q", svc.Dashboard)
	}
}

func TestServicesFor_NoControllerErr(t *testing.T) {
	if _, err := servicesFor(context.Background(), "", ""); err != errNoController {
		t.Errorf("empty controller URL should report ErrNoController; got %v", err)
	}
}

func TestServicesFor_SendsBearerToken(t *testing.T) {
	resetCache()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(services{Dashboard: "x"})
	}))
	defer srv.Close()

	if _, err := servicesFor(context.Background(), srv.URL, "swu_abc"); err != nil {
		t.Fatalf("ServicesFor: %v", err)
	}
	if gotAuth != "Bearer swu_abc" {
		t.Errorf("Authorization = %q, want Bearer swu_abc", gotAuth)
	}
}
