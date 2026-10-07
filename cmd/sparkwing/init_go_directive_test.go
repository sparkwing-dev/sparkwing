package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/mod/modfile"
)

func TestPipelineScaffoldDoesNotRequireGoPatch(t *testing.T) {
	repo := enterScaffoldTestRepo(t)
	tools := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n'env GOVERSION') printf 'go1.26.8\\n';;\n'env GOTOOLCHAIN GOVERSION GOENV') printf 'auto\\ngo1.26.8\\noff\\n';;\nesac\n"
	if err := os.WriteFile(filepath.Join(tools, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := runPipelineNew([]string{"--name", "sample", "--template", "minimal"}); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(repo, ".sparkwing", "go.mod")
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	mod, err := modfile.Parse(name, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mod.Go == nil || mod.Go.Version != "1.26" {
		t.Fatalf("scaffold go directive = %v, want minor-only floor for patch-version Go", mod.Go)
	}
}
