// Package apiroutes reads the HTTP route table out of the controller and
// logs-service routing source, so every generated API reference describes
// the routes and scopes the servers actually register.
//
// Parse recognises the registration form those files use,
// `mux.Handle("METHOD /path", requireScope(ScopeX, ...))`, and resolves the
// scope constant through the map [Scopes] collects from the auth sources:
//
//	scopes, err := apiroutes.Scopes("pkg/controller/auth.go")
//	routes, err := apiroutes.Parse("pkg/controller/server.go", scopes)
package apiroutes

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Public marks a route registered outside the authentication middleware, so
// it answers without any credential.
const Public = "public"

// Authenticated marks a route behind the authentication middleware that
// declares no further scope, so any valid bearer satisfies it.
const Authenticated = "authenticated"

var (
	handleRE = regexp.MustCompile(`(\w+)\.Handle(?:Func)?\("([A-Z]+) (/[^"]+)",\s*(.*)$`)

	scopeRefRE = regexp.MustCompile(`requireScope\((\w+),`)

	scopeConstRE = regexp.MustCompile(`\b([A-Za-z]\w*)\s*=\s*"([a-z][a-z.]*)"`)
)

// Route is one registered HTTP route: its method, its pattern as the router
// spells it, and the scope its handler chain demands.
type Route struct {
	Method string
	Path   string
	Scope  string
}

// Scopes collects the scope constants declared in the given Go sources,
// keyed by constant name (`ScopeRunsRead`) and valued by wire scope
// (`runs.read`).
func Scopes(files ...string) (map[string]string, error) {
	scopes := map[string]string{}
	for _, file := range files {
		// #nosec G703 -- a build-time tool reading paths the operator names
		// #nosec G703 -- a build-time tool reading paths the operator names
	data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read scopes: %w", err)
		}
		for _, m := range scopeConstRE.FindAllStringSubmatch(string(data), -1) {
			if strings.HasPrefix(m[1], "Scope") || strings.HasPrefix(m[1], "scope") {
				scopes[m[1]] = m[2]
			}
		}
	}
	return scopes, nil
}

// Parse returns every route registered in file, sorted by path then method.
// A route registered on the `mux` receiver sits behind the authentication
// middleware; anything else is reachable without a credential.
func Parse(file string, scopes map[string]string) ([]Route, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read routes: %w", err)
	}
	var routes []Route
	for line := range strings.SplitSeq(string(data), "\n") {
		m := handleRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		receiver, method, path, rest := m[1], m[2], m[3], m[4]
		scope := Public
		if receiver == "mux" {
			scope = Authenticated
		}
		if sm := scopeRefRE.FindStringSubmatch(rest); sm != nil {
			if v, ok := scopes[sm[1]]; ok {
				scope = v
			} else {
				scope = sm[1]
			}
		}
		routes = append(routes, Route{Method: method, Path: path, Scope: scope})
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes, nil
}
