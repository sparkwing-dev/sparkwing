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

func unsupportedRouteServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": controller.UnsupportedRouteError,
			"route": r.Method + " " + r.URL.Path,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func plainNotFoundServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such row", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func clientCalls() []struct {
	name string
	call func(context.Context, *client.Client) error
} {
	return []struct {
		name string
		call func(context.Context, *client.Client) error
	}{
		{"CreateRun", func(ctx context.Context, c *client.Client) error { return c.CreateRun(ctx, store.Run{ID: "r1"}) }},
		{"GetRun", func(ctx context.Context, c *client.Client) error { _, err := c.GetRun(ctx, "r1"); return err }},
		{"GetRunForExecution", func(ctx context.Context, c *client.Client) error {
			_, err := c.GetRunForExecution(ctx, "r1")
			return err
		}},
		{"GetRunReceipt", func(ctx context.Context, c *client.Client) error { _, err := c.GetRunReceipt(ctx, "r1"); return err }},
		{"GetLatestRun", func(ctx context.Context, c *client.Client) error {
			_, err := c.GetLatestRun(ctx, "p", nil, 0)
			return err
		}},
		{"CancelRun", func(ctx context.Context, c *client.Client) error { return c.CancelRun(ctx, "r1") }},
		{"DeleteRun", func(ctx context.Context, c *client.Client) error { return c.DeleteRun(ctx, "r1") }},
		{"RetryRun", func(ctx context.Context, c *client.Client) error { _, err := c.RetryRun(ctx, "r1", false); return err }},
		{"GetNode", func(ctx context.Context, c *client.Client) error { _, err := c.GetNode(ctx, "r1", "n1"); return err }},
		{"GetNodeOutput", func(ctx context.Context, c *client.Client) error {
			_, err := c.GetNodeOutput(ctx, "r1", "n1")
			return err
		}},
		{"GetNodeDispatch", func(ctx context.Context, c *client.Client) error {
			_, err := c.GetNodeDispatch(ctx, "r1", "n1", 1)
			return err
		}},
		{"ListNodeDispatches", func(ctx context.Context, c *client.Client) error {
			_, err := c.ListNodeDispatches(ctx, "r1", "n1")
			return err
		}},
		{"GetApproval", func(ctx context.Context, c *client.Client) error {
			_, err := c.GetApproval(ctx, "r1", "n1")
			return err
		}},
		{"ResolveApproval", func(ctx context.Context, c *client.Client) error {
			_, err := c.ResolveApproval(ctx, "r1", "n1", "approved", "me", "")
			return err
		}},
		{"GetActiveDebugPause", func(ctx context.Context, c *client.Client) error {
			_, err := c.GetActiveDebugPause(ctx, "r1", "n1")
			return err
		}},
		{"GetTrigger", func(ctx context.Context, c *client.Client) error { _, err := c.GetTrigger(ctx, "t1"); return err }},
		{"HeartbeatTrigger", func(ctx context.Context, c *client.Client) error { _, err := c.HeartbeatTrigger(ctx, "t1"); return err }},
		{"GetPipelineProfile", func(ctx context.Context, c *client.Client) error {
			_, err := c.GetPipelineProfile(ctx, "p", "n1")
			return err
		}},
		{"ObserveSlot", func(ctx context.Context, c *client.Client) error { _, err := c.ObserveSlot(ctx, "k", "h"); return err }},
		{"ConcurrencyState", func(ctx context.Context, c *client.Client) error { _, err := c.ConcurrencyState(ctx, "k"); return err }},
		{"GetSecret", func(ctx context.Context, c *client.Client) error { _, err := c.GetSecret(ctx, "s"); return err }},
		{"DeleteSecretForRepo", func(ctx context.Context, c *client.Client) error { return c.DeleteSecretForRepo(ctx, "s", "") }},
	}
}

func TestEveryHelperReportsAnUnsupportedRoute(t *testing.T) {
	srv := unsupportedRouteServer(t)
	for _, tc := range clientCalls() {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(context.Background(), client.New(srv.URL, srv.Client()))
			if !errors.Is(err, client.ErrControllerLacksRoute) {
				t.Fatalf("%s error = %v, want ErrControllerLacksRoute", tc.name, err)
			}
			if errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s reports a route the controller lacks as a missing row", tc.name)
			}
		})
	}
}

func TestAPlainNotFoundKeepsItsMeaning(t *testing.T) {
	srv := plainNotFoundServer(t)
	nilOn404 := map[string]bool{"DeleteRun": true, "GetPipelineProfile": true}
	for _, tc := range clientCalls() {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(context.Background(), client.New(srv.URL, srv.Client()))
			if errors.Is(err, client.ErrControllerLacksRoute) {
				t.Fatalf("%s read an ordinary 404 as a missing route: %v", tc.name, err)
			}
			if nilOn404[tc.name] {
				if err != nil {
					t.Errorf("%s error = %v, want nil for an ordinary 404", tc.name, err)
				}
				return
			}
			if tc.name == "CreateRun" {
				return
			}
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s error = %v, want store.ErrNotFound for an ordinary 404", tc.name, err)
			}
		})
	}
}
