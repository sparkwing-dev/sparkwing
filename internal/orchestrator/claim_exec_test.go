package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

// The pipeline's build takes only the module settings the init container may
// name from the file it left, whatever else the file holds.
func TestApplyGoEnvFile_ReadsOnlyTheModuleSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), GoEnvFile)
	if err := os.WriteFile(path, []byte("GOPRIVATE=github.com/acme/plans\nGOFLAGS=-toolexec=/tmp/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOFLAGS", "-mod=mod")
	if err := applyGoEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("GOPRIVATE"); got != "github.com/acme/plans" {
		t.Fatalf("GOPRIVATE = %q", got)
	}
	if got := os.Getenv("GOFLAGS"); got != "-mod=mod" {
		t.Fatalf("GOFLAGS = %q; the file set a setting it may not name", got)
	}
	if err := applyGoEnvFile(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("a checkout with no listed repository left no file: %v", err)
	}
}
