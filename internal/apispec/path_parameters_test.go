package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apiroutes"
)

func TestRewriteSharesTemplatedPathsAcrossMethods(t *testing.T) {
	spec := `openapi: 3.0.3
info: {title: t, version: "0.1"}
paths:
  /api/v1/runs/{run}:
    parameters:
      - {in: path, name: run, required: true, schema: {type: string}}
    get:
      summary: Read status.
      responses:
        "200": {description: OK}
    delete:
      summary: Delete run.
      responses:
        "204": {description: Deleted}
components: {}
`
	routes := []apiroutes.Route{
		{Method: "GET", Path: "/api/v1/runs/{run}", Scope: "runs.read"},
		{Method: "DELETE", Path: "/api/v1/runs/{id}", Scope: "admin"},
	}
	out, err := rewrite(spec, routes, []string{"runs.read", "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "/api/v1/runs/{id}:") || strings.Contains(out, stubSummary) || !strings.Contains(out, "x-sparkwing-scope: admin") || !strings.Contains(out, "x-sparkwing-scope: runs.read") {
		t.Fatalf("equivalent parameter names produced separate paths:\n%s", out)
	}
	withoutDelete := strings.Replace(spec, "    delete:\n      summary: Delete run.\n      responses:\n        \"204\": {description: Deleted}\n", "", 1)
	seeded, err := rewrite(withoutDelete, routes, nil)
	if err != nil || strings.Contains(seeded, "/api/v1/runs/{id}:") || !strings.Contains(seeded, "    delete:") {
		t.Fatalf("missing method was seeded on a second path: %v\n%s", err, seeded)
	}
	duplicate := strings.Replace(spec, "components: {}", "  /api/v1/runs/{id}: {}\ncomponents: {}", 1)
	if _, err := rewrite(duplicate, routes, nil); err == nil {
		t.Fatal("duplicate templated paths were accepted")
	}
	if _, err := rewrite(out, routes[:1], nil); err == nil {
		t.Fatal("unregistered method was accepted")
	}
	routes[1].Path = "/api/v1/teams/{id}"
	if _, err := rewrite(out, routes, nil); err == nil {
		t.Fatal("different resource was accepted")
	}
}
