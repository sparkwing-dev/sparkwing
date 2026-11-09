package sdkchecks

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypeScriptHasNoMaskerOrAutomaticRegistration(t *testing.T) {
	tsRoot := filepath.Join("..", "..", "sdk", "typescript", "src")
	if _, err := os.Stat(filepath.Join(tsRoot, "diagnostics.ts")); !os.IsNotExist(err) {
		t.Fatal("TypeScript SDK retains diagnostics.ts masker")
	}
	scanned := 0
	if err := filepath.WalkDir(tsRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".ts") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, forbidden := range []string{"PlanningDiagnostics", "Masker", ".mask("} {
			if forbidden == ".mask(" && filepath.Base(path) == "context.ts" {
				continue
			}
			if strings.Contains(string(raw), forbidden) {
				t.Errorf("TypeScript %s retains %s", path, forbidden)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("found no TypeScript SDK sources to scan")
	}
}
