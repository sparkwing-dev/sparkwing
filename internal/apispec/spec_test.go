package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sparkwing-dev/sparkwing/internal/apiroutes"
)

const regenerateSpec = "bash bin/gen-api-docs.sh"

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

func realSpec(t *testing.T) (spec string, routes []apiroutes.Route, scopes []string) {
	t.Helper()
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	names, err := apiroutes.Scopes(filepath.Join(root, "pkg", "controller", "auth.go"))
	if err != nil {
		t.Fatalf("read scopes: %v", err)
	}
	routes, err = apiroutes.Parse(filepath.Join(root, "pkg", "controller", "server.go"), names)
	if err != nil {
		t.Fatalf("read routes: %v", err)
	}
	if len(routes) == 0 {
		t.Fatal("the controller route table parsed as empty")
	}
	return string(body), routes, scopeValues(names)
}

func TestGeneratorIsDeterministicOnTheCommittedSpec(t *testing.T) {
	spec, routes, scopes := realSpec(t)
	once, err := rewrite(spec, routes, scopes)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	twice, err := rewrite(once, routes, scopes)
	if err != nil {
		t.Fatalf("second rewrite: %v", err)
	}
	if once != twice {
		t.Error("two rewrites of the same inputs disagree; the generated spec is not a fixed point")
	}
	if once != spec {
		t.Errorf("api/openapi.yaml disagrees with the route table; regenerate with `%s`", regenerateSpec)
	}
}

func TestEveryRegisteredRouteIsDocumented(t *testing.T) {
	spec, routes, _ := realSpec(t)
	documented := documentedOperations(t, spec)
	for _, r := range routes {
		key := r.Method + " " + specPath(r.Path)
		if !documented[key] {
			t.Errorf("route %s is registered but absent from api/openapi.yaml; regenerate with `%s`", key, regenerateSpec)
		}
	}
}

func TestAssistedClaimSpecKeepsRefusalBoundary(t *testing.T) {
	spec, _, _ := realSpec(t)
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(stripHeader(spec)), &doc); err != nil {
		t.Fatal(err)
	}
	paths := mapValue(doc.Content[0], "paths")
	prepare := mapValue(mapValue(paths, "/api/v1/nodes/claim/prepare"), "post")
	prepareDescription := mapValue(prepare, "description")
	responses := mapValue(prepare, "responses")
	conflict := mapValue(mapValue(responses, "409"), "description")
	for name, node := range map[string]*yaml.Node{
		"prepare description": prepareDescription,
		"409 description":     conflict,
	} {
		if node == nil {
			t.Fatalf("%s is absent", name)
		}
	}
	if !strings.Contains(prepareDescription.Value, "409 body_attestation_required") ||
		!strings.Contains(conflict.Value, "No capacity has been reserved") {
		t.Fatalf("assisted prepare contract drifted: description=%q 409=%q",
			prepareDescription.Value, conflict.Value)
	}
	if success := mapValue(responses, "200"); success != nil {
		t.Fatalf("unattested prepare documents an unreachable success response: %+v", success)
	}
	claim := mapValue(mapValue(paths, "/api/v1/nodes/claim"), "post")
	if description := mapValue(claim, "description"); description == nil ||
		!strings.Contains(description.Value, "before reservation or award") {
		t.Fatalf("assisted offer boundary is absent from claim description: %+v", description)
	}
}

func documentedOperations(t *testing.T, spec string) map[string]bool {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(stripHeader(spec)), &doc); err != nil {
		t.Fatalf("parse api/openapi.yaml: %v", err)
	}
	paths := mapValue(doc.Content[0], "paths")
	if paths == nil {
		t.Fatal("api/openapi.yaml has no paths mapping")
	}
	out := map[string]bool{}
	for i := 0; i+1 < len(paths.Content); i += 2 {
		path, item := paths.Content[i].Value, paths.Content[i+1]
		for j := 0; j+1 < len(item.Content); j += 2 {
			method := strings.ToLower(item.Content[j].Value)
			if isMethod(method) {
				out[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	return out
}
