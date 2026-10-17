package apiroutes

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseOperatorEndpointsUsesActualRegistrations(t *testing.T) {
	src := writeFile(t, "server.go", "s.operatorEndpoints()")
	endpoints := `{Method: "GET", Pattern: "/api/v1/operator/limits", Handle: s.limits},
{Method: "GET", Pattern: "/api/v1/operator/teams", Handle: s.teams},
{Method: "GET", Pattern: "/api/v1/operator/object-store", Handle: s.storage},`
	path := filepath.Join(filepath.Dir(src), "operator_routes.go")
	if err := os.WriteFile(path, []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Method: "GET", Path: "/api/v1/operator/limits", Scope: "operator.limits` or `operator"},
		{Method: "GET", Path: "/api/v1/operator/object-store", Scope: "operator.object_store` or `operator"},
		{Method: "GET", Path: "/api/v1/operator/teams", Scope: Operator},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operator registrations = %+v, want %+v", got, want)
	}
	if err := os.WriteFile(path, []byte(`{Method: "GET", Pattern: "/unclassified", Handle: s.unclassified},`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(src, nil); err == nil {
		t.Fatal("operator endpoint without policy was documented")
	}
}

func TestParseCapabilityEndpointsUsesActualRegistrations(t *testing.T) {
	src := writeFile(t, "server.go", "s.capabilityEndpoints()")
	endpoints := `{Method: "GET", Pattern: "/api/v1/tokens"},
{Method: "GET", Pattern: "/api/v1/operator/tokens/{token}"},
{Method: "POST", Pattern: "/api/v1/operator/tokens/{token}/rotate"},`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "capability_management.go"), []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Method: "GET", Path: "/api/v1/operator/tokens/{token}", Scope: "operator.tokens` or `operator"},
		{Method: "POST", Path: "/api/v1/operator/tokens/{token}/rotate", Scope: "operator.tokens` or `operator"},
		{Method: "GET", Path: "/api/v1/tokens", Scope: "runs.read"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("capability registrations=%+v want %+v", got, want)
	}
}

func TestParseGitHubRunnerEndpointsUsesActualRegistrations(t *testing.T) {
	src := writeFile(t, "server.go", "s.githubRunnerEndpoints()")
	endpoints := `{Method: "GET", Pattern: "/api/v1/runners/github"},
{Method: "POST", Pattern: "/api/v1/runners/github"},
{Method: "DELETE", Pattern: "/api/v1/runners/github/{repository}"},`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "github_runner_admin.go"), []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Method: "GET", Path: "/api/v1/runners/github", Scope: "runs.read"},
		{Method: "POST", Path: "/api/v1/runners/github", Scope: "team.admin"},
		{Method: "DELETE", Path: "/api/v1/runners/github/{repository}", Scope: "team.admin"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GitHub runner registrations=%+v want %+v", got, want)
	}
}

func TestParseEngineEndpointsUsesActualRegistrations(t *testing.T) {
	src := writeFile(t, "server.go", "s.engineEndpoints()")
	endpoints := `{
		Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/expansion", Authentication: capability.AuthCapability,
	}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "engine_routes.go"), []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{{Method: "POST", Path: "/api/v1/runs/{run}/jobs/{job}/expansion", Scope: "expansion.submit"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("engine registrations = %+v, want %+v", got, want)
	}
}

func TestParseQueueEndpointUsesActualRegistration(t *testing.T) {
	src := writeFile(t, "server.go", "s.queueEndpoints()")
	endpoint := `{Method: "GET", Pattern: "/api/v1/queue", Authentication: capability.AuthCapability}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "queue_view.go"), []byte(endpoint), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Route{{Method: "GET", Path: "/api/v1/queue", Scope: "runs.read"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queue registration=%+v want=%+v", got, want)
	}
}

func TestParseBrowserSessionEndpointUsesNamedProof(t *testing.T) {
	src := writeFile(t, "browser.go", "s.registerPolicyEndpoints(mux, s.browserSessionEndpoints())")
	endpoint := `{Method: "POST", Pattern: "/logout", Authentication: capability.AuthBrowserFlow}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "browser_session_routes.go"), []byte(endpoint), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Route{{Method: "POST", Path: "/logout", Scope: "browser_flow_csrf_nonce"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("browser session registration=%+v want=%+v", got, want)
	}
}

func TestParseOAuthEndpointsUsesNamedProof(t *testing.T) {
	src := writeFile(t, "browser.go", "s.registerPolicyEndpoints(mux, s.oauthEndpoints())")
	endpoints := `{Method: "GET", Pattern: "/auth/{provider}/start", Authentication: capability.AuthBrowserFlow},
{Method: "GET", Pattern: "/auth/{provider}/callback", Authentication: capability.AuthBrowserFlow},`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "browser_oauth_routes.go"), []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Method: "GET", Path: "/auth/{provider}/callback", Scope: "browser_flow_csrf_nonce"},
		{Method: "GET", Path: "/auth/{provider}/start", Scope: "browser_flow_csrf_nonce"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OAuth registration=%+v want=%+v", got, want)
	}
}

func TestParseIdentityLinkEndpointsUsesNamedProof(t *testing.T) {
	src := writeFile(t, "browser.go", "s.registerPolicyEndpoints(mux, s.identityLinkEndpoints())")
	endpoints := `{Method: "POST", Pattern: "/auth/{provider}/link", Authentication: capability.AuthBrowserFlow},
{Method: "GET", Pattern: "/auth/{provider}/link/complete", Authentication: capability.AuthBrowserFlow},`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "identity_link_routes.go"), []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Method: "POST", Path: "/auth/{provider}/link", Scope: "browser_flow_csrf_nonce"},
		{Method: "GET", Path: "/auth/{provider}/link/complete", Scope: "browser_flow_csrf_nonce"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("link registration=%+v want=%+v", got, want)
	}
}

func TestParseSourceEndpointsUsesActualRegistrations(t *testing.T) {
	src := writeFile(t, "server.go", "s.sourceEndpoints()")
	endpoints := `{Method: "POST", Pattern: "/api/v1/runs/{run}/source-credential", Authentication: capability.AuthCapability},
{Method: "PUT", Pattern: "/api/v1/tokens/{token}/git-credentials", Authentication: capability.AuthCapability},`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "source_controller.go"), []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{{Method: "POST", Path: "/api/v1/runs/{run}/source-credential", Scope: "source.read"}, {Method: "PUT", Path: "/api/v1/tokens/{token}/git-credentials", Scope: Authenticated}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source registrations=%+v want=%+v", got, want)
	}
}

func TestParseAuthorityEndpointsUsesActualRegistrations(t *testing.T) {
	src := writeFile(t, "server.go", "s.authorityEndpoints()")
	endpoints := `{Method: "DELETE", Pattern: "/api/v1/tokens/{token}", Authentication: capability.AuthCapability},
{Method: "DELETE", Pattern: "/api/v1/operator/tokens/{token}", Authentication: capability.AuthCapability},
{Method: "PATCH", Pattern: "/api/v1/team/members/{account}", Authentication: capability.AuthCapability},
{Method: "GET", Pattern: "/api/v1/auth/whoami", Authentication: capability.AuthCapability},
{Method: "GET", Pattern: "/api/v1/services", Authentication: capability.AuthCapability},
{Method: "GET", Pattern: "/api/v1/data/capabilities", Authentication: capability.AuthCapability},
{Method: "GET", Pattern: "/.well-known/openid-configuration", Authentication: capability.AuthPublic},
{Method: "GET", Pattern: "/.well-known/jwks.json", Authentication: capability.AuthPublic},`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "authority_routes.go"), []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Method: "GET", Path: "/.well-known/jwks.json", Scope: Public},
		{Method: "GET", Path: "/.well-known/openid-configuration", Scope: Public},
		{Method: "GET", Path: "/api/v1/auth/whoami", Scope: Authenticated},
		{Method: "GET", Path: "/api/v1/data/capabilities", Scope: Authenticated},
		{Method: "DELETE", Path: "/api/v1/operator/tokens/{token}", Scope: "operator.tokens` or `operator"},
		{Method: "GET", Path: "/api/v1/services", Scope: Authenticated},
		{Method: "PATCH", Path: "/api/v1/team/members/{account}", Scope: "team.admin"},
		{Method: "DELETE", Path: "/api/v1/tokens/{token}", Scope: "runs.read"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authority registrations = %+v, want %+v", got, want)
	}
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScopes(t *testing.T) {
	src := writeFile(t, "auth.go", `package controller

const (
	ScopeRunsRead  = "runs.read"
	ScopeNodesClaim = "nodes.claim"
	notAScope      = "hello"
)

var ScopeAll = "admin"
`)
	got, err := Scopes(src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ScopeRunsRead":   "runs.read",
		"ScopeNodesClaim": "nodes.claim",
		"ScopeAll":        "admin",
	}
	if len(got) != len(want) {
		t.Fatalf("scopes = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("scopes[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestParse(t *testing.T) {
	src := writeFile(t, "server.go", `package controller

func (s *Server) routes() {
	mux.Handle("POST /api/v1/runs", requireScope(ScopeRunsState, http.HandlerFunc(s.handleCreateRun)))
	mux.Handle("GET /api/v1/runs/{id}", requireScope(ScopeRunsRead, s.readableRun(http.HandlerFunc(s.handleGetRun))))
	mux.Handle("GET /api/v1/auth/whoami", http.HandlerFunc(s.handleWhoami))
	mux.Handle("GET /api/v1/gitcache/git/{path...}", requireScope(ScopeUnknown, http.HandlerFunc(s.handleGitcacheGit)))
	mux.Handle("GET /api/v1/triggers/{id}", requireScope(ScopeTriggersRead, s.readableTrigger(http.HandlerFunc(s.handleGetTrigger)), ScopeNodesClaim, ScopeTriggersClaim))
	router.HandleFunc("GET /api/v1/health", s.handleHealth)
	log.Printf("not a route")
}
`)
	scopes := map[string]string{"ScopeRunsState": "runs.state", "ScopeRunsRead": "runs.read", "ScopeTriggersRead": "triggers.read", "ScopeNodesClaim": "nodes.claim", "ScopeTriggersClaim": "triggers.claim"}
	got, err := Parse(src, scopes)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Method: "GET", Path: "/api/v1/auth/whoami", Scope: Authenticated},
		{Method: "GET", Path: "/api/v1/gitcache/git/{path...}", Scope: "ScopeUnknown"},
		{Method: "GET", Path: "/api/v1/health", Scope: Public},
		{Method: "POST", Path: "/api/v1/runs", Scope: "runs.state"},
		{Method: "GET", Path: "/api/v1/runs/{id}", Scope: "runs.read"},
		{Method: "GET", Path: "/api/v1/triggers/{id}", Scope: "triggers.read` or `nodes.claim` or `triggers.claim"},
	}
	if len(got) != len(want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("routes[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestParseSizingEndpointsUsesActualRegistrations(t *testing.T) {
	src := writeFile(t, "server.go", "s.sizingEndpoints()")
	endpoints := `
	{Method: "GET", Pattern: "/api/v1/pipelines/{name}/sizing"},
	{Method: "PUT", Pattern: "/api/v1/pipelines/{name}/sizing/pin"},
	{Method: "POST", Pattern: "/api/v1/pipelines/{name}/sizing/observations"},
	{Method: "POST", Pattern: "/api/v1/pipelines/{name}/sizing/contention"},
	{Method: "POST", Pattern: "/api/v1/pipelines/{name}/sizing/waits"},`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "sizing_routes.go"), []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Method: "GET", Path: "/api/v1/pipelines/{name}/sizing", Scope: "runs.read` or `source.read"},
		{Method: "POST", Path: "/api/v1/pipelines/{name}/sizing/contention", Scope: "node.report"},
		{Method: "POST", Path: "/api/v1/pipelines/{name}/sizing/observations", Scope: "node.report"},
		{Method: "PUT", Path: "/api/v1/pipelines/{name}/sizing/pin", Scope: "node.report"},
		{Method: "POST", Path: "/api/v1/pipelines/{name}/sizing/waits", Scope: "node.report"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sizing registrations=%+v want %+v", got, want)
	}
}

func TestParseCredentialEndpoints(t *testing.T) {
	src := writeFile(t, "server.go", "s.credentialEndpoints()")
	endpoints := `
{Method: "GET", Pattern: "/api/v1/secrets"},
{Method: "POST", Pattern: "/api/v1/secrets"},
{Method: "DELETE", Pattern: "/api/v1/secrets/{name}"},
{Method: "GET", Pattern: "/api/v1/team/git-credentials"},
{Method: "POST", Pattern: "/api/v1/team/git-credentials"},
{Method: "POST", Pattern: "/api/v1/team/git-credentials/{host}/confirm"},
{Method: "DELETE", Pattern: "/api/v1/team/git-credentials/{host}"},`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "credential_inventory.go"), []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Method: "GET", Path: "/api/v1/secrets", Scope: "runs.read` or `team.admin"},
		{Method: "POST", Path: "/api/v1/secrets", Scope: "team.admin"},
		{Method: "DELETE", Path: "/api/v1/secrets/{name}", Scope: "team.admin"},
		{Method: "GET", Path: "/api/v1/team/git-credentials", Scope: "runs.read"},
		{Method: "POST", Path: "/api/v1/team/git-credentials", Scope: "team.admin"},
		{Method: "DELETE", Path: "/api/v1/team/git-credentials/{host}", Scope: "team.admin"},
		{Method: "POST", Path: "/api/v1/team/git-credentials/{host}/confirm", Scope: "team.admin"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inventory registrations=%+v want %+v", got, want)
	}
}

func TestParseMissingFile(t *testing.T) {
	if _, err := Parse(filepath.Join(t.TempDir(), "absent.go"), nil); err == nil {
		t.Fatal("Parse on a missing file returned no error")
	}
}

func TestParseCreditEndpointsUsesActualRegistrations(t *testing.T) {
	src := writeFile(t, "server.go", "s.creditEndpoints()")
	endpoints := `
	{Method: "GET", Pattern: "/api/v1/credits"},
	{Method: "GET", Pattern: "/api/v1/credits/history"},`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "credit_routes.go"), []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{{Method: "GET", Path: "/api/v1/credits", Scope: "runs.read"}, {Method: "GET", Path: "/api/v1/credits/history", Scope: "runs.read"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("credit registrations=%+v want %+v", got, want)
	}
}

func TestParseDataEndpointsUsesActualRegistrations(t *testing.T) {
	src := writeFile(t, "server.go", "s.dataEndpoints()")
	endpoints := `{Method: "POST", Pattern: "/api/v1/data/download", Authentication: capability.AuthCapability},
 {Method: "POST", Pattern: "/api/v1/data/has-many", Authentication: capability.AuthCapability},`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "data_routes.go"), []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Method: "POST", Path: "/api/v1/data/download", Scope: "data.read"},
		{Method: "POST", Path: "/api/v1/data/has-many", Scope: "data.read"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("data registration=%+v want=%+v", got, want)
	}
}

func TestParseActionsEndpointUsesActualProof(t *testing.T) {
	src := writeFile(t, "server.go", "s.actionsEndpoints()")
	endpoints := `{Method: "POST", Pattern: "/api/v1/runners/github/exchange", Authentication: capability.AuthGitHubActions}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "github_actions_exchange.go"), []byte(endpoints), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{{Method: "POST", Path: "/api/v1/runners/github/exchange", Scope: "github_actions_oidc"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Actions registration=%+v want=%+v", got, want)
	}
}

func TestParseConcurrencyEndpointsUsesActualRegistrations(t *testing.T) {
	src := writeFile(t, "server.go", "s.concurrencyEndpoints()")
	data, err := os.ReadFile(filepath.Join("..", "..", "pkg", "controller", "concurrency_routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(src), "concurrency_routes.go"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	routes, err := Parse(src, nil)
	if err != nil || len(routes) != 8 {
		t.Fatalf("routes=%+v error=%v", routes, err)
	}
	for _, r := range routes {
		if r.Path == "/api/v1/queue/groups/{group}/force-release" && r.Scope != "runs.control" {
			t.Fatalf("force-release=%+v", r)
		}
	}
}

func TestParseControllerBusinessRegistrations(t *testing.T) {
	routes, err := Parse("../../pkg/controller/server.go", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"DELETE /api/v1/runs/{run}":                                   false,
		"PATCH /api/v1/team":                                          false,
		"DELETE /api/v1/team":                                         false,
		"POST /api/v1/team/invitations":                               false,
		"DELETE /api/v1/team/invitations/{invitation}":                false,
		"PUT /api/v1/team/build-trust":                                false,
		"DELETE /api/v1/team/github-app/installations/{installation}": false,
		"PUT /api/v1/team/github-app/triggers":                        false,
		"DELETE /api/v1/team/github-app/triggers":                     false,
		"GET /api/v1/team/github-app/automation":                      false,
		"PUT /api/v1/team/github-app/automation":                      false,
		"DELETE /api/v1/team/github-app/automation":                   false,
		"PUT /api/v1/team/github-app/extra-repos":                     false,
		"POST /api/v1/team/billing/checkout":                          false,
		"POST /api/v1/team/billing/card":                              false,
		"POST /api/v1/team/billing/pay":                               false,
		"PUT /api/v1/team/billing/budget":                             false,

		"GET /api/v1/schedules":                      false,
		"DELETE /api/v1/schedules/{schedule}":        false,
		"POST /api/v1/schedules/{schedule}/run":      false,
		"GET /api/v1/team/billing":                   false,
		"POST /api/v1/runs/{run}/approvals/{job}":    false,
		"POST /api/v1/runs/{run}/debug-pauses":       false,
		"GET /metrics":                               false,
		"GET /api/v1/runners":                        false,
		"POST /api/v1/runners":                       false,
		"POST /api/v1/runs/{run}/jobs/{job}/metrics": false,
		"POST /api/v1/runs/{run}/jobs/{job}/usage":   false,
	}
	for _, route := range routes {
		key := route.Method + " " + route.Path
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("registration missing from discovery: %s", key)
		}
	}
}
