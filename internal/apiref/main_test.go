package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRoutesListsAcceptedScopes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.go")
	source := `mux.Handle("GET /api/v1/runs/{id}", requireScope(ScopeRunsRead, handler, ScopeNodesClaim, ScopeTriggersClaim))`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	writeRoutes(&out, "Controller", map[string]string{
		"ScopeRunsRead":      "runs.read",
		"ScopeNodesClaim":    "nodes.claim",
		"ScopeTriggersClaim": "triggers.claim",
	}, path)
	want := "| `GET` | `/api/v1/runs/{id}` | `runs.read` or `nodes.claim` or `triggers.claim` |"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("generated route table missing %q:\n%s", want, out.String())
	}
}
