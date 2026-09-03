package orchestrator

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apiroutes"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the writing handle is the default, so this list carries no runtime
// weight; it exists to make the classification exhaustive, so a new
// controller route fails the gate below instead of defaulting silently.
var apiWriteRoutes = []string{
	"GET /api/v1/agents",
	"GET /api/v1/auth/bootstrap-needed",
	"POST /api/v1/auth/login",
	"POST /api/v1/auth/logout",
	"GET /api/v1/auth/session",
	"GET /api/v1/auth/whoami",
	"POST /api/v1/concurrency/{key}/acquire",
	"POST /api/v1/concurrency/{key}/cancel-waiter",
	"POST /api/v1/concurrency/{key}/force-release",
	"POST /api/v1/concurrency/{key}/heartbeat",
	"POST /api/v1/concurrency/{key}/release",
	"GET /api/v1/concurrency/{key}/resolve",
	"POST /api/v1/gitcache/refresh",
	"POST /api/v1/nodes/claim",
	"PUT /api/v1/pipelines/{name}/profile/pin",
	"GET /api/v1/pool",
	"POST /api/v1/pool/checkout",
	"POST /api/v1/pool/heartbeat",
	"POST /api/v1/pool/return",
	"GET /api/v1/queue/state",
	"POST /api/v1/runs",
	"DELETE /api/v1/runs/{id}",
	"POST /api/v1/runs/{id}/approvals/{nodeID}",
	"POST /api/v1/runs/{id}/approvals/{nodeID}/request",
	"GET /api/v1/runs/{id}/attempts",
	"POST /api/v1/runs/{id}/cancel",
	"POST /api/v1/runs/{id}/debug-pauses",
	"POST /api/v1/runs/{id}/events",
	"POST /api/v1/runs/{id}/finish",
	"POST /api/v1/runs/{id}/heartbeat",
	"POST /api/v1/runs/{id}/nodes",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/activity",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/annotations",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/artifact-manifest",
	"GET /api/v1/runs/{id}/nodes/{nodeID}/bounce",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/bounce",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/bounce/consume",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/deps",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/dispatch",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/finish",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/heartbeat",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/mark-ready",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/metrics",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/release",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/revoke-ready",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/start",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/status",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/annotations",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/finish",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/skip",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/start",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/summary",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/summary",
	"POST /api/v1/runs/{id}/nodes/{nodeID}/touch",
	"POST /api/v1/runs/{id}/plan",
	"GET /api/v1/runs/{id}/receipt",
	"POST /api/v1/runs/{id}/retry",
	"GET /api/v1/secrets",
	"POST /api/v1/secrets",
	"DELETE /api/v1/secrets/{name}",
	"GET /api/v1/secrets/{name}",
	"GET /api/v1/services",
	"GET /api/v1/tokens",
	"POST /api/v1/tokens",
	"DELETE /api/v1/tokens/{prefix}",
	"GET /api/v1/tokens/{prefix}",
	"POST /api/v1/tokens/{prefix}/rotate",
	"GET /api/v1/trends",
	"POST /api/v1/triggers",
	"POST /api/v1/triggers/claim",
	"POST /api/v1/triggers/{id}/done",
	"POST /api/v1/triggers/{id}/heartbeat",
	"GET /api/v1/users",
	"POST /api/v1/users",
	"DELETE /api/v1/users/{name}",
	"GET /metrics",
	"POST /webhooks/github/{pipeline}",
}

func TestEveryControllerRouteIsClassified(t *testing.T) {
	scopes, err := apiroutes.Scopes("../../pkg/controller/auth.go")
	if err != nil {
		t.Fatalf("read the controller's scopes: %v", err)
	}
	registered, err := apiroutes.Parse("../../pkg/controller/server.go", scopes)
	if err != nil {
		t.Fatalf("read the controller's route table: %v", err)
	}
	classified := map[string]string{}
	add := func(kind string, routes []string) {
		for _, route := range routes {
			if prior, seen := classified[route]; seen {
				t.Errorf("%s is classified both %s and %s", route, prior, kind)
			}
			classified[route] = kind
		}
	}
	add("read", apiReadRoutes)
	add("stream", apiStreamRoutes)
	add("local", apiLocalRoutes)
	add("write", apiWriteRoutes)

	for _, r := range registered {
		route := r.Method + " " + r.Path
		kind, ok := classified[route]
		if !ok {
			t.Errorf("the controller registers %s and the daemon's API classifies it nowhere; add it to apiReadRoutes, apiStreamRoutes, apiLocalRoutes, or apiWriteRoutes", route)
			continue
		}
		delete(classified, route)
		// safety: the lists are documentation until the runtime classifiers
		// answer the same question, and a wildcard pattern can classify a
		// literal route the lists file under something else.
		req, err := http.NewRequest(r.Method, fillRoutePattern(r.Path), nil)
		if err != nil {
			t.Errorf("build a request for %s: %v", route, err)
			continue
		}
		if got, want := streamingRoute(req), kind == "stream"; got != want {
			t.Errorf("%s is classified %s and streamingRoute reports %v; the runtime classifier and the lists disagree", route, kind, got)
		}
		if got, want := localRoute(req), kind == "local"; got != want {
			t.Errorf("%s is classified %s and localRoute reports %v", route, kind, got)
		}
	}
	for route, kind := range classified {
		t.Errorf("the daemon's API classifies %s as %s and the controller registers no such route", route, kind)
	}
}

func TestReadRoutesAnswerFromTheReadOnlyHandle(t *testing.T) {
	home := wingdTestHome(t)
	createStore(t, home)
	sock, _ := startAPIDaemon(t, home, nil)
	httpClient := apiHTTPClient(sock)
	c := client.New(apiBaseURL, httpClient)
	seedRun(t, c, "r1", "n1")
	conc := NewHTTPConcurrency(apiBaseURL, httpClient, "", 30*time.Second)
	if _, err := conc.AcquireSlot(context.Background(), store.AcquireSlotRequest{
		Key: "memo:m1", RunID: "r1", NodeID: "n1", HolderID: "h1",
		Policy: "memoize", Lease: 30 * time.Second,
	}); err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}

	for _, route := range apiReadRoutes {
		method, pattern, ok := strings.Cut(route, " ")
		if !ok {
			t.Fatalf("route %q has no method", route)
		}
		path := fillRoutePattern(pattern)
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequest(method, apiBaseURL+path, nil)
			if err != nil {
				t.Fatalf("build the request: %v", err)
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			// safety: a read route wrongly served from the read-only handle
			// fails with a write error rather than a status a client models,
			// so any 5xx is the signal, not the exact code.
			if resp.StatusCode >= 500 {
				t.Errorf("%s %s answered %d from the read-only handle", method, path, resp.StatusCode)
			}
		})
	}
}

func fillRoutePattern(pattern string) string {
	values := map[string]string{
		"{id}":       "r1",
		"{nodeID}":   "n1",
		"{key}":      url.PathEscape("memo:m1"),
		"{name}":     "p",
		"{prefix}":   "sws_none",
		"{path...}":  "repo/info/refs",
		"{pipeline}": "p",
	}
	parts := strings.Split(pattern, "/")
	for i, part := range parts {
		if value, ok := values[part]; ok {
			parts[i] = value
		}
	}
	return strings.Join(parts, "/")
}

func TestReadRouteListCarriesNoDuplicates(t *testing.T) {
	sorted := slices.Clone(apiReadRoutes)
	slices.Sort(sorted)
	if slices.Compact(sorted) == nil {
		t.Fatal("the read route list is empty")
	}
	if len(slices.Compact(slices.Clone(sorted))) != len(apiReadRoutes) {
		t.Fatal("the read route list repeats a route")
	}
}
