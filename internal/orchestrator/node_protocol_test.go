package orchestrator

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apiroutes"
)

type specRoute struct{ method, path, reach string }

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
			reach:  strings.TrimSpace(cells[3]),
		})
	}
	if len(routes) == 0 {
		t.Fatal("parsed no routes from docs/node-protocol.md")
	}
	return routes
}

var routeWildcard = regexp.MustCompile(`\{[^}]*\}`)

// hack: the spec, the broker and each server name wildcards differently, so the
// names are erased before comparing.
func normalizeRoute(method, path string) string {
	return method + " " + routeWildcard.ReplaceAllStringFunc(path, func(w string) string {
		if strings.HasSuffix(w, "...}") {
			return "{...}"
		}
		return "{}"
	})
}

func TestNodeProtocolSpecListsExactlyTheBrokerAllowlist(t *testing.T) {
	spec := map[string]bool{}
	for _, r := range nodeProtocolSpecRoutes(t) {
		switch r.reach {
		case "broker":
			spec[r.method+" "+r.path] = true
		case "direct", "target":
		default:
			t.Errorf("docs/node-protocol.md: %s %s has reach %q, want broker, direct or target", r.method, r.path, r.reach)
		}
	}
	allowed := map[string]bool{}
	for _, r := range brokerRoutes {
		allowed[r.method+" "+r.pattern] = true
	}
	for _, missing := range sortedDifference(allowed, spec) {
		t.Errorf("the broker allows %s, which docs/node-protocol.md does not list as a broker route", missing)
	}
	for _, extra := range sortedDifference(spec, allowed) {
		t.Errorf("docs/node-protocol.md lists %s as a broker route, which the broker does not allow", extra)
	}
}

func TestNodeProtocolSpecRoutesMatchTheRegisteredHandlers(t *testing.T) {
	registered := map[string]bool{}
	for _, file := range []string{
		filepath.Join("..", "..", "pkg", "controller", "server.go"),
		filepath.Join("..", "..", "pkg", "logs", "server.go"),
	} {
		routes, err := apiroutes.Parse(file, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range routes {
			registered[normalizeRoute(r.Method, r.Path)] = true
		}
	}
	for _, r := range nodeProtocolSpecRoutes(t) {
		key := normalizeRoute(r.method, r.path)
		brokerServed := strings.HasPrefix(r.path, "/bin/")
		switch {
		case r.reach == "target" && registered[key]:
			t.Errorf("docs/node-protocol.md marks %s %s as target, but a server registers it; mark it broker or direct", r.method, r.path)
		case r.reach != "target" && !brokerServed && !registered[key]:
			t.Errorf("docs/node-protocol.md lists %s %s, which neither the controller nor the logs service registers", r.method, r.path)
		}
	}
}

func sortedDifference(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
