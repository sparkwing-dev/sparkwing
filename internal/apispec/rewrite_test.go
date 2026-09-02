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

func TestRewriteRejectsAMalformedResponseObject(t *testing.T) {
	spec := strings.Replace(minimalSpec,
		`        "200": {description: OK}`,
		`        "200": {description: Missing repo URL, sha: '', or workspace flag.: ''}`, 1)
	routes := []apiroutes.Route{{Method: "GET", Path: "/api/v1/runs", Scope: "runs.read"}}
	_, err := rewrite(spec, routes, nil)
	if err == nil {
		t.Fatal("rewrite accepted a response object an unquoted comma split into three fields")
	}
	for _, want := range []string{"GET /api/v1/runs", "200", "sha"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
}

func TestRewriteAllowsTheResponseObjectFields(t *testing.T) {
	spec := strings.Replace(minimalSpec,
		`        "200": {description: OK}`,
		`        "200":
          description: OK
          x-note: fine
          headers: {}
          content: {}
          links: {}`, 1)
	routes := []apiroutes.Route{{Method: "GET", Path: "/api/v1/runs", Scope: "runs.read"}}
	if _, err := rewrite(spec, routes, nil); err != nil {
		t.Fatalf("rewrite rejected a well-formed response object: %v", err)
	}
}

func TestRewriteRejectsABareScopeToken(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"requires admin", "      summary: List runs. Requires admin.\n"},
		{"admin only", "      summary: Admin-only.\n"},
		{"admin token", "      description: Requires an admin token.\n"},
		{"admits one scope", "      description: Admits `runs.read` only.\n"},
		{"needs a scope", "      description: Needs runs.write.\n"},
		{"holds a claim scope", "      description: Caller must hold `nodes.claim`.\n"},
		{"scope permission", "      description: Requires runs.read permission.\n"},
	}
	routes := []apiroutes.Route{{Method: "GET", Path: "/api/v1/runs", Scope: "runs.read"}}
	scopes := []string{"admin", "nodes.claim", "runs.read", "runs.write"}
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

func TestRewriteRejectsScopeProseAwayFromTheOperationRoot(t *testing.T) {
	cases := []struct {
		name string
		spec string
	}{
		{"path item", strings.Replace(minimalSpec, "  /api/v1/runs:\n",
			"  /api/v1/runs:\n    description: Requires admin.\n", 1)},
		{"external docs", strings.Replace(minimalSpec, "      summary: List runs.\n",
			"      summary: List runs.\n      externalDocs: {description: Needs runs.write., url: 'https://example.test'}\n", 1)},
		{"response", strings.Replace(minimalSpec, `        "200": {description: OK}`,
			`        "200": {description: Served to an admin caller.}`, 1)},
	}
	routes := []apiroutes.Route{{Method: "GET", Path: "/api/v1/runs", Scope: "runs.read"}}
	scopes := []string{"admin", "runs.read", "runs.write"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := rewrite(tc.spec, routes, scopes); err == nil {
				t.Fatal("rewrite accepted a scope stated in nested prose")
			}
		})
	}
}

func TestRewriteAllowsAPerFieldNarrowing(t *testing.T) {
	spec := strings.Replace(minimalSpec, "      summary: List runs.\n",
		"      summary: List runs.\n      description: |\n"+
			"        Admits `runs.read`, but fills `env_json` only for an\n"+
			"        `admin` principal; every other reader gets the row without it.\n", 1)
	routes := []apiroutes.Route{{Method: "GET", Path: "/api/v1/runs", Scope: "runs.read"}}
	if _, err := rewrite(spec, routes, []string{"admin", "runs.read"}); err != nil {
		t.Fatalf("rewrite rejected an allow-listed per-field narrowing: %v", err)
	}
}

func TestRewriteIgnoresScopeProseInASchema(t *testing.T) {
	spec := strings.Replace(minimalSpec, "      summary: List runs.\n",
		"      summary: List runs.\n      requestBody:\n        content:\n"+
			"          application/json:\n            schema:\n"+
			"              properties: {scopes: {description: Omit to grant admin.}}\n", 1)
	routes := []apiroutes.Route{{Method: "GET", Path: "/api/v1/runs", Scope: "runs.read"}}
	if _, err := rewrite(spec, routes, []string{"admin", "runs.read"}); err != nil {
		t.Fatalf("rewrite rejected a schema member's own documentation: %v", err)
	}
}
