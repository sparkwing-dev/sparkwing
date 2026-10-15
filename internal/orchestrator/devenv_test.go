package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDevEnv(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "dev.env"), []byte(body), 0o600); err != nil {
		t.Fatalf("write dev.env: %v", err)
	}
	return root
}

func TestReadDevEnvParsesTheFallbackFile(t *testing.T) {
	root := writeDevEnv(t, "# a comment\n\nSPARKWING_CONTROLLER_URL = http://127.0.0.1:4344 \nbroken\n")

	values := readDevEnv(root)
	if got := values["SPARKWING_CONTROLLER_URL"]; got != "http://127.0.0.1:4344" {
		t.Fatalf("controller URL = %q, want the value the file carries", got)
	}
	if len(values) != 1 {
		t.Errorf("parsed %d entries, want only the one assignment: %v", len(values), values)
	}
}

func TestReadDevEnvIsEmptyWithoutAFile(t *testing.T) {
	if values := readDevEnv(t.TempDir()); len(values) != 0 {
		t.Fatalf("a home holding no dev.env resolved %v", values)
	}
}
