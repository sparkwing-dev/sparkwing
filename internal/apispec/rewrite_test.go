package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apiroutes"
)

const minimalSpec = `openapi: 3.0.3
info:
  title: t
  version: "0.1"
paths:
  /api/v1/runs:
    get:
      summary: List runs.
      responses:
        "200": {description: OK}
components: {}
`

func TestRewriteStampsScope(t *testing.T) {
	routes := []apiroutes.Route{{Method: "GET", Path: "/api/v1/runs", Scope: "runs.read"}}
	out, err := rewrite(minimalSpec, routes, []string{"runs.read"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "x-sparkwing-scope: runs.read") {
		t.Errorf("rewritten spec carries no scope:\n%s", out)
	}
	if !strings.HasPrefix(out, "# GENERATED IN PART") {
		t.Errorf("rewritten spec carries no banner:\n%s", out[:80])
	}
}

func TestRewriteRestampsAChangedScope(t *testing.T) {
	stale := strings.Replace(minimalSpec, "    get:\n", "    get:\n      x-sparkwing-scope: admin\n", 1)
	routes := []apiroutes.Route{{Method: "GET", Path: "/api/v1/runs", Scope: "runs.read"}}
	out, err := rewrite(stale, routes, []string{"admin", "runs.read"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "x-sparkwing-scope: admin") {
		t.Errorf("rewritten spec kept the stale scope:\n%s", out)
	}
	if !strings.Contains(out, "x-sparkwing-scope: runs.read") {
		t.Errorf("rewritten spec carries no scope:\n%s", out)
	}
}

func TestRewriteIsIdempotent(t *testing.T) {
	routes := []apiroutes.Route{
		{Method: "GET", Path: "/api/v1/runs", Scope: "runs.read"},
		{Method: "GET", Path: "/api/v1/gitcache/git/{path...}", Scope: "admin"},
	}
	once, err := rewrite(minimalSpec, routes, nil)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := rewrite(once, routes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Errorf("second rewrite differs from the first:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestRewriteSeedsAnUndocumentedRoute(t *testing.T) {
	routes := []apiroutes.Route{
		{Method: "GET", Path: "/api/v1/runs", Scope: "runs.read"},
		{Method: "POST", Path: "/api/v1/gitcache/git/{path...}", Scope: "admin"},
	}
	out, err := rewrite(minimalSpec, routes, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/api/v1/gitcache/git/{path}:",
		stubSummary,
		"name: path",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("seeded stub is missing %q:\n%s", want, out)
		}
	}
}

func TestRewriteRejectsAnUnregisteredOperation(t *testing.T) {
	_, err := rewrite(minimalSpec, nil, nil)
	if err == nil {
		t.Fatal("rewrite accepted a documented route the router does not register")
	}
	if !strings.Contains(err.Error(), "GET /api/v1/runs") {
		t.Errorf("error does not name the route: %v", err)
	}
}

func TestRewriteRejectsScopeProse(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"summary", "      summary: List runs. Admin scope.\n"},
		{"description", "      description: Requires the `runs.read` scope.\n"},
		{"hyphenated", "      summary: List runs. Nodes-claim scope.\n"},
	}
	routes := []apiroutes.Route{{Method: "GET", Path: "/api/v1/runs", Scope: "runs.read"}}
	scopes := []string{"admin", "nodes.claim", "runs.read"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := strings.Replace(minimalSpec, "      summary: List runs.\n", tc.field, 1)
			_, err := rewrite(spec, routes, scopes)
			if err == nil {
				t.Fatal("rewrite accepted a scope stated in prose")
			}
			if !strings.Contains(err.Error(), "GET /api/v1/runs") {
				t.Errorf("error does not name the route: %v", err)
			}
		})
	}
}

func TestRewriteAllowsProseWithoutAScopeClaim(t *testing.T) {
	spec := strings.Replace(minimalSpec, "      summary: List runs.\n",
		"      summary: List runs newest first.\n", 1)
	routes := []apiroutes.Route{{Method: "GET", Path: "/api/v1/runs", Scope: "runs.read"}}
	if _, err := rewrite(spec, routes, []string{"admin", "runs.read"}); err != nil {
		t.Fatalf("rewrite rejected prose that names no scope: %v", err)
	}
}
