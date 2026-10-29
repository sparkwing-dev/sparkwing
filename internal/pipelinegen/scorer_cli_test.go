package pipelinegen

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestProjectScorerCLIHelper(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		return
	}
	invocation, err := clisurface.Parse("sparkwing", os.Args[separator+1:])
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Value("pipeline") != "candidate" {
		t.Fatalf("scorer selected pipeline %q", invocation.Value("pipeline"))
	}
}

func TestProjectScorerUsesCurrentCLIGrammar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command adapter")
	}
	base := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":  "module example.com/scorer\n\ngo 1.26.0\n",
		"go.sum":  "",
		"main.go": "package main\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adapter := filepath.Join(t.TempDir(), "sparkwing")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run '^TestProjectScorerCLIHelper$' -- \"$@\"\n"
	if err := os.WriteFile(adapter, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	checks, err := NewProjectScorer(adapter, base).Score(t.Context(), Spec{Name: "candidate", Entrypoint: "Candidate"}, "package jobs\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range checks {
		if !check.OK {
			t.Errorf("%s: %s", check.Name, check.Detail)
		}
	}
}
