package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestUnregisteredRouteAnswersJSONNamingTheRoute(t *testing.T) {
	srv := httptest.NewServer(controller.New(nil, nil).Handler())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/api/v1/does-not-exist")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content type = %q, want application/json", got)
	}
	var body struct {
		Error string `json:"error"`
		Route string `json:"route"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != controller.UnsupportedRouteError {
		t.Errorf("error = %q, want %q", body.Error, controller.UnsupportedRouteError)
	}
	if want := "GET /api/v1/does-not-exist"; body.Route != want {
		t.Errorf("route = %q, want %q", body.Route, want)
	}
}

func TestRegisteredPathKeepsItsMethodNotAllowed(t *testing.T) {
	srv := httptest.NewServer(controller.New(nil, nil).Handler())
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, srv.URL+"/api/v1/runs", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 for a path registered under other methods", resp.StatusCode)
	}
	if resp.Header.Get("Allow") == "" {
		t.Error("405 answer carries no Allow header")
	}
}

func TestClientRecognisesAnUnsupportedRoute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": controller.UnsupportedRouteError,
			"route": r.Method + " " + r.URL.Path,
		})
	}))
	defer srv.Close()

	err := client.New(srv.URL, srv.Client()).CreateRun(context.Background(), store.Run{ID: "r1"})
	if !errors.Is(err, client.ErrControllerLacksRoute) {
		t.Fatalf("error = %v, want ErrControllerLacksRoute", err)
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Error("a route the controller does not register must not read as a missing row")
	}
	if !strings.Contains(err.Error(), "POST /api/v1/runs") {
		t.Errorf("error %q does not name the route the controller lacks", err)
	}
}
