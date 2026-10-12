package orchestrator

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apiroutes"
)

type specRoute struct{ method, path string }

func nodeProtocolSpecRoutes(t *testing.T) []specRoute {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "node-protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, table, ok := strings.Cut(string(raw), "<!-- node-routes:start -->")
	table, _, ok2 := strings.Cut(table, "<!-- node-routes:end -->")
	if !ok || !ok2 {
		t.Fatal("docs/node-protocol.md lost its node-routes markers")
	}
	var routes []specRoute
	for line := range strings.SplitSeq(table, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 4 || !strings.HasPrefix(strings.TrimSpace(cells[2]), "`") {
			continue
		}
		routes = append(routes, specRoute{
			method: strings.TrimSpace(cells[1]),
			path:   strings.Trim(strings.TrimSpace(cells[2]), "`"),
		})
	}
	if len(routes) == 0 {
		t.Fatal("parsed no routes from docs/node-protocol.md")
	}
	return routes
}

var routeWildcard = regexp.MustCompile(`\{[^}]*\}`)

// hack: the spec and each server name wildcards differently, so the names are
// erased before comparing.
func normalizeRoute(method, path string) string {
	return method + " " + routeWildcard.ReplaceAllStringFunc(path, func(w string) string {
		if strings.HasSuffix(w, "...}") {
			return "{...}"
		}
		return "{}"
	})
}

func TestNodeProtocolRoutesAreTheNodeNamespace(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range nodeProtocolSpecRoutes(t) {
		key := normalizeRoute(r.method, r.path)
		switch {
		case !strings.HasPrefix(r.path, "/node/v1/"):
			t.Errorf("docs/node-protocol.md lists %s %s outside /node/v1/", r.method, r.path)
		case r.method != "GET" && r.method != "POST" && r.method != "DELETE":
			t.Errorf("docs/node-protocol.md lists %s %s with a method the node routes do not use", r.method, r.path)
		case seen[key]:
			t.Errorf("docs/node-protocol.md lists %s %s twice", r.method, r.path)
		}
		seen[key] = true
	}
}

// safety: the node routes carry a bearer scoped to one attempt, so a controller
// route on the same path would accept a credential it was never meant to see.
func TestNodeProtocolRoutesAreNotControllerRoutes(t *testing.T) {
	routes, err := apiroutes.Parse(filepath.Join("..", "..", "pkg", "controller", "server.go"), nil)
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, r := range routes {
		registered[normalizeRoute(r.Method, r.Path)] = true
	}
	for _, r := range nodeProtocolSpecRoutes(t) {
		if registered[normalizeRoute(r.method, r.path)] {
			t.Errorf("the controller registers %s %s, a node route only the node host serves", r.method, r.path)
		}
	}
}
