package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapUsesExistingPipelineModule(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	module := "module example.com/existing/pipelines\n\ngo 1.26.0\n\nreplace github.com/sparkwing-dev/sparkwing/sparkwing => /example/sdk\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapDotSparkwing(root, dir); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"example.com/existing/pipelines/jobs"`) {
		t.Fatalf("main import=%s", body)
	}
	body, err = os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil || string(body) != module {
		t.Fatalf("module changed: %s error=%v", body, err)
	}
}
