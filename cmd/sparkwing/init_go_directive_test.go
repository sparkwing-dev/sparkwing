package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testshell"

	"golang.org/x/mod/modfile"
)

func TestPipelineScaffoldDoesNotRequireGoPatch(t *testing.T) {
	repo := enterScaffoldTestRepo(t)
	tools := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n'env GOVERSION') printf 'go1.26.8\\n';;\n'env GOTOOLCHAIN GOVERSION GOENV') printf 'auto\\ngo1.26.8\\noff\\n';;\nesac\n"
	testshell.Install(t, filepath.Join(tools, "go"), script)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := parsePipelineNew([]string{"--pipeline", "sample", "--template", "minimal"}); err != nil {
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
	if mod.Go == nil || mod.Go.Version != "1.25.0" {
		t.Fatalf("scaffold go directive = %v, want SDK minimum independent of the installed Go patch", mod.Go)
	}
}
