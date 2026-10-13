package orchestrator

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPipelineRequiresFallsBackToTheProjectDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "defaults:\n  requires: [linux]\npipelines:\n  - name: plain\n    entrypoint: Plain\n  - name: gpu\n    entrypoint: GPU\n    requires: [gpu]\n"
	if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "sparkwing.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pipeline string
		want     []string
	}{
		{"plain", []string{"linux"}},
		{"gpu", []string{"gpu"}},
		{"absent", nil},
	} {
		got, err := PipelineRequires(dir, tc.pipeline)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s requires %v, want %v: a pod and a local run must place the pipeline alike", tc.pipeline, got, tc.want)
		}
	}
}
