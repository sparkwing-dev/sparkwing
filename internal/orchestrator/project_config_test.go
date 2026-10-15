package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func TestHostedProjectDefaultsWithoutPipelineEntry(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"), []byte("defaults:\n  args: {mode: default}\n  guards: {when: [where:controller]}\n  requires: [linux]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{Pipeline: "build"}
	selection, err := applyHostedProjectConfig(&opts, root)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Invoked != "build" || selection.Entrypoint != "build" {
		t.Fatal("default guards changed pipeline selection:", selection)
	}
	if mergeInvokeArgs(opts)["mode"] != "default" {
		t.Fatal("default arguments lost without pipeline entry")
	}
	if opts.PipelineYAML == nil {
		t.Fatal("default guards lost without pipeline entry")
	}
	if err := opts.PipelineYAML.Guards.Evaluate("build", pipelines.GuardContext{ProfileIsLocal: true}); err == nil {
		t.Fatal("default guard did not refuse local execution")
	}
	requires, err := PipelineRequires(root, "build")
	if err != nil || len(requires) != 1 || requires[0] != "linux" {
		t.Fatalf("default placement lost: %v %v", requires, err)
	}
}
