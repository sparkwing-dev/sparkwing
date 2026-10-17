package bincache

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMaterializePipelineUsesTheCompilationWorkspace(t *testing.T) {
	for _, covering := range []bool{true, false} {
		t.Run(map[bool]string{true: "covering", false: "unrelated"}[covering], func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("SPARKWING_HOME", filepath.Join(root, "home"))
			t.Setenv("GOPROXY", "off")
			directory := filepath.Join(root, "pipeline")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{"go.mod": "module fixture\ngo 1.26\n", "main.go": "package main\nfunc main(){print(\"workspace pipeline\")}\n"} {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			use := "./pipeline"
			if !covering {
				use = "./unrelated"
			}
			work := filepath.Join(root, "go.work")
			if err := os.WriteFile(work, []byte("go 1.26\nuse "+use+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GOWORK", work)
			var diagnostics bytes.Buffer
			lease, _, err := MaterializePipeline(t.Context(), directory, CompileOptions{Environment: os.Environ(), Diagnostics: &diagnostics})
			if err != nil {
				t.Fatalf("workspace materialization: %v: %s", err, &diagnostics)
			}
			t.Cleanup(func() {
				if err := lease.Release(); err != nil {
					t.Error(err)
				}
			})
			result, err := exec.CommandContext(t.Context(), lease.Path()).CombinedOutput()
			if err != nil || string(result) != "workspace pipeline" {
				t.Fatalf("materialized workspace pipeline: %q %v", result, err)
			}
		})
	}
}
