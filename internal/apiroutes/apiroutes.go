// Package apiroutes reads the HTTP route table out of the controller's
// routing source, so every generated API reference describes the routes and
// scopes the server actually registers.
//
// parse recognises the registration form those files use,
// `mux.Handle("METHOD /path", requireScope(ScopeX, ...))`, and resolves the
// scope constant through the map [scopes] collects from the auth sources.
package apiroutes

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// public marks a route registered outside the authentication middleware, so
// it answers without any credential.
const public = "public"

// authenticated marks a route behind the authentication middleware that
// declares no further scope, so any valid bearer satisfies it.
const authenticated = "authenticated"

// claim marks a route that answers a claim token, the credential one node
// claim holds. A claim route that also names a scope answers either.
const claim = "claim"

// operator marks a route only a listed operator account's own browser session
// reaches. No token satisfies it, an admin token included.
const operator = "operator"

var (
	handleRE   = regexp.MustCompile(`(\w+)\.Handle(?:Func)?\("([A-Z]+) (/[^"]+)",\s*(.*)$`)
	endpointRE = regexp.MustCompile(`\{\s*Method: "([A-Z]+)", Pattern: "(/[^"]+)"`)

	scopeRefRE = regexp.MustCompile(`\b((?:Scope|scope)[A-Za-z]\w*)\b`)

	scopeConstRE = regexp.MustCompile(`\b([A-Za-z]\w*)\s*=\s*"([a-z][a-z.]*)"`)
)

// route is one registered HTTP route: its method, its pattern as the router
// spells it, and the scope its handler chain demands.
type route struct {
	Method string
	Path   string
	Scope  string
}

// scopes collects the scope constants declared in the given Go sources,
// keyed by constant name (`ScopeRunsRead`) and valued by wire scope
// (`runs.read`).
func scopes(files ...string) (map[string]string, error) {
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

// parse returns registered routes sorted by path then method, using policy for endpoint authentication.
func parse(file string, scopes map[string]string) ([]route, error) {
	// #nosec G703 -- a build-time tool reading paths the operator names
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read routes: %w", err)
	}
	var routes []route
	for _, group := range []struct{ registration, file string }{
		{"s.operatorEndpoints()", "operator_routes.go"},
		{"s.engineEndpoints()", "engine_routes.go"},
		{"s.claimEndpoints()", "engine_claim_routes.go"},
		{"s.authorityEndpoints()", "authority_routes.go"},
		{"s.capabilityEndpoints()", "capability_management.go"},
		{"s.credentialEndpoints()", "credential_inventory.go"},
		{"s.sizingEndpoints()", "sizing_routes.go"},
		{"s.creditEndpoints()", "credit_routes.go"},
		{"s.queueEndpoints()", "queue_view.go"},
		{"s.concurrencyEndpoints()", "concurrency_routes.go"},
		{"s.dataEndpoints()", "data_routes.go"},
		{"s.sourceEndpoints()", "source_controller.go"},
		{"s.actionsEndpoints()", "github_actions_exchange.go"},
		{"s.githubRunnerEndpoints()", "github_runner_admin.go"},
		{"s.sessionEndpoints()", "session_routes.go"},
		{"s.accountEndpoints()", "account_routes.go"},
		{"s.reportingEndpoints()", "reporting_routes.go"},
		{"s.metricEndpoints()", "metrics.go"},
		{"s.teamReadEndpoints()", "team_read_routes.go"},
		{"s.scheduleEndpoints()", "schedule_routes.go"},
		{"s.runControlEndpoints()", "run_control_routes.go"},
		{"s.teamWriteEndpoints()", "team_write_routes.go"},
		{"s.githubAppEndpoints()", "github_app_routes.go"},
		{"s.billingEndpoints()", "billing_routes.go"},
		{"s.registerPolicyEndpoints(mux, s.browserSessionEndpoints())", "browser_session_routes.go"},
		{"s.registerPolicyEndpoints(mux, s.loginEndpoints())", "browser_login_routes.go"},
		{"s.registerPolicyEndpoints(mux, s.oauthEndpoints())", "browser_oauth_routes.go"},
		{"s.registerPolicyEndpoints(mux, s.identityLinkEndpoints())", "identity_link_routes.go"},
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
					alternatives = append(alternatives, operator)
					continue
				}
				if len(access.Grants) == 0 {
					alternatives = append(alternatives, authenticated)
					continue
				}
				var grants []string
				for _, grant := range access.Grants {
					grants = append(grants, string(grant))
				}
				alternatives = append(alternatives, strings.Join(grants, " + "))
			}
			scope := strings.Join(slices.Compact(alternatives), "` or `")
			if policy.Authentication == capability.AuthPublic {
				scope = public
			} else if policy.Authentication != capability.AuthCapability {
				scope = string(policy.Authentication)
			}
			routes = append(routes, route{Method: match[1], Path: match[2], Scope: scope})
		}
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		m := handleRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		receiver, method, path, rest := m[1], m[2], m[3], m[4]
		scope := public
		if receiver == "mux" {
			scope = authenticated
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
			scope = operator
		}
		if receiver == "mux" && strings.Contains(rest, "newClaim") {
			if scope == authenticated {
				scope = claim
			} else {
				scope = claim + "` or `" + scope
			}
		}
		routes = append(routes, route{Method: method, Path: path, Scope: scope})
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes, nil
}
