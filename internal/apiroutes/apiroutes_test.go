package apiroutes

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseOperatorEndpointsUsesActualRegistrations(t *testing.T) {
	src := writeFile(t, "server.go", "s.registerOperatorRoutes(router)")
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

func TestParseEngineEndpointsUsesActualRegistrations(t *testing.T) {
	src := writeFile(t, "server.go", "s.registerEngineRoutes(router)")
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
	mux.Handle("GET /api/v1/runs/{id}", requireScope(ScopeRunsRead, s.reconcileBeforeRead(s.handleGetRun)))
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

func TestParseMissingFile(t *testing.T) {
	if _, err := Parse(filepath.Join(t.TempDir(), "absent.go"), nil); err == nil {
		t.Fatal("Parse on a missing file returned no error")
	}
}
