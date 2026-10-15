// Package apiroutes reads the HTTP route table out of the controller's
// routing source, so every generated API reference describes the routes and
// scopes the server actually registers.
//
// Parse recognises the registration form those files use,
// `mux.Handle("METHOD /path", requireScope(ScopeX, ...))`, and resolves the
// scope constant through the map [Scopes] collects from the auth sources.
package apiroutes

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// Public marks a route registered outside the authentication middleware, so
// it answers without any credential.
const Public = "public"

// Authenticated marks a route behind the authentication middleware that
// declares no further scope, so any valid bearer satisfies it.
const Authenticated = "authenticated"

// Claim marks a route that answers a claim token, the credential one node
// claim holds. A claim route that also names a scope answers either.
const Claim = "claim"

// Operator marks a route only a listed operator account's own browser session
// reaches. No token satisfies it, an admin token included.
const Operator = "operator"

var (
	handleRE   = regexp.MustCompile(`(\w+)\.Handle(?:Func)?\("([A-Z]+) (/[^"]+)",\s*(.*)$`)
	endpointRE = regexp.MustCompile(`\{\s*Method: "([A-Z]+)", Pattern: "(/[^"]+)"`)

	scopeRefRE = regexp.MustCompile(`\b((?:Scope|scope)[A-Za-z]\w*)\b`)

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
	// #nosec G703 -- a build-time tool reading paths the operator names
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read routes: %w", err)
	}
	var routes []Route
	for _, group := range []struct{ registration, file string }{
		{"s.registerOperatorRoutes(router)", "operator_routes.go"},
		{"s.registerEngineRoutes(router)", "engine_routes.go"},
		{"s.registerPolicyEndpoints(router, s.claimEndpoints())", "engine_claim_routes.go"},
		{"s.registerAuthorityRoutes(router)", "authority_routes.go"},
		{"s.registerPolicyEndpoints(router, s.capabilityEndpoints())", "capability_management.go"},
		{"s.registerCredentialRoutes(router)", "credential_inventory.go"},
		{"s.registerPolicyEndpoints(router, s.sizingEndpoints())", "sizing_routes.go"},
		{"s.registerPolicyEndpoints(router, s.creditEndpoints())", "credit_routes.go"},
		{"s.registerPolicyEndpoints(router, s.queueEndpoints())", "queue_view.go"},
		{"s.registerPolicyEndpoints(router, s.concurrencyEndpoints())", "concurrency_routes.go"},
		{"s.registerPolicyEndpoints(router, s.dataEndpoints())", "data_routes.go"},
		{"s.registerPolicyEndpoints(router, s.sourceEndpoints())", "source_controller.go"},
		{"s.registerPolicyEndpoints(router, s.actionsEndpoints())", "github_actions_exchange.go"},
		{"s.registerPolicyEndpoints(router, s.githubRunnerEndpoints())", "github_runner_admin.go"},
		{"s.registerPolicyEndpoints(router, s.sessionEndpoints())", "session_routes.go"},
		{"s.registerAccountRoutes(router)", "account_routes.go"},
		{"s.registerPolicyEndpoints(mux, s.browserSessionEndpoints())", "browser_session_routes.go"},
		{"s.registerPolicyEndpoints(mux, s.loginEndpoints())", "browser_login_routes.go"},
		{"s.registerPolicyEndpoints(mux, s.oauthEndpoints())", "browser_oauth_routes.go"},
	} {
		if !strings.Contains(string(data), group.registration) {
			continue
		}
		// safety: the reference follows actual endpoint declarations, independently of the policy table.
		endpoints, err := os.ReadFile(filepath.Join(filepath.Dir(file), group.file))
		if err != nil {
			return nil, fmt.Errorf("read endpoints: %w", err)
		}
		for _, match := range endpointRE.FindAllStringSubmatch(string(endpoints), -1) {
			policy, ok := capability.LookupRoute(match[1], match[2])
			if !ok {
				return nil, fmt.Errorf("endpoint lacks policy: %s %s", match[1], match[2])
			}
			var alternatives []string
			for _, access := range policy.Access {
				if access.Credential == capability.CredentialOperator {
					alternatives = append(alternatives, Operator)
					continue
				}
				if len(access.Grants) == 0 {
					alternatives = append(alternatives, Authenticated)
					continue
				}
				var grants []string
				for _, grant := range access.Grants {
					grants = append(grants, string(grant))
				}
				alternatives = append(alternatives, strings.Join(grants, " + "))
			}
			scope := strings.Join(alternatives, "` or `")
			if policy.Authentication == capability.AuthPublic {
				scope = Public
			} else if policy.Authentication != capability.AuthCapability {
				scope = string(policy.Authentication)
			}
			routes = append(routes, Route{Method: match[1], Path: match[2], Scope: scope})
		}
	}
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
		if matches := scopeRefRE.FindAllStringSubmatch(rest, -1); len(matches) > 0 {
			seen := map[string]bool{}
			var accepted []string
			for _, match := range matches {
				value := scopes[match[1]]
				if value == "" {
					value = match[1]
				}
				if !seen[value] {
					accepted = append(accepted, value)
					seen[value] = true
				}
			}
			scope = strings.Join(accepted, "` or `")
		}
		if receiver == "mux" && strings.Contains(rest, "requireOperator(") {
			scope = Operator
		}
		if receiver == "mux" && strings.Contains(rest, "newClaim") {
			if scope == Authenticated {
				scope = Claim
			} else {
				scope = Claim + "` or `" + scope
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
