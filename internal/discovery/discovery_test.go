package discovery_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/discovery"
)

func TestServicesFor_ReturnsAnnouncedCachePod(t *testing.T) {
	discovery.ResetCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/services" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(discovery.Services{CachePod: "https://cache.example.dev"})
	}))
	defer srv.Close()

	svc, err := discovery.ServicesFor(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatalf("ServicesFor: %v", err)
	}
	if svc.CachePod != "https://cache.example.dev" {
		t.Errorf("CachePod = %q", svc.CachePod)
	}
}

func TestServicesFor_CachesAcrossCalls(t *testing.T) {
	discovery.ResetCache()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(discovery.Services{CachePod: "https://x"})
	}))
	defer srv.Close()

	for range 5 {
		if _, err := discovery.ServicesFor(context.Background(), srv.URL, "t"); err != nil {
			t.Fatalf("ServicesFor: %v", err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("controller hit %d times across 5 calls; want 1 (cached)", got)
	}
}

func TestServicesFor_404IsNoErrorNoCachePod(t *testing.T) {
	discovery.ResetCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	svc, err := discovery.ServicesFor(context.Background(), srv.URL, "")
	if err != nil {
		t.Errorf("404 should report no error (caller falls back); got %v", err)
	}
	if svc.CachePod != "" {
		t.Errorf("CachePod should be empty on 404; got %q", svc.CachePod)
	}
}

func TestServicesFor_NoControllerErr(t *testing.T) {
	if _, err := discovery.ServicesFor(context.Background(), "", ""); err != discovery.ErrNoController {
		t.Errorf("empty controller URL should report ErrNoController; got %v", err)
	}
}

func TestServicesFor_SendsBearerToken(t *testing.T) {
	discovery.ResetCache()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(discovery.Services{CachePod: "x"})
	}))
	defer srv.Close()

	if _, err := discovery.ServicesFor(context.Background(), srv.URL, "swu_abc"); err != nil {
		t.Fatalf("ServicesFor: %v", err)
	}
	if gotAuth != "Bearer swu_abc" {
		t.Errorf("Authorization = %q, want Bearer swu_abc", gotAuth)
	}
}
