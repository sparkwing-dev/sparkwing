package apiroutes

import (
	"os"
	"path/filepath"
	"testing"
)

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
	scopes := map[string]string{"ScopeRunsState": "runs.state", "ScopeRunsRead": "runs.read", "ScopeTriggersRead": "triggers.read"}
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
		{Method: "GET", Path: "/api/v1/triggers/{id}", Scope: "triggers.read"},
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
