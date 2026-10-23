package bincache

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializePipelineReusesBinaryWithoutGo(t *testing.T) {
	dir := newPipelineDir(t)
	t.Setenv("SPARKWING_HOME", filepath.Join(t.TempDir(), "home"))
	env := []string{"PATH=" + t.TempDir(), "GOWORK=off"}
	key, _, err := ExplainCacheKey(dir, env)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := PipelineEntry(key)
	if err != nil {
		t.Fatal(err)
	}
	seed, _, err := entry.AcquireOrMaterialize(t.Context(), func(path string) error {
		return os.WriteFile(path, []byte("cached pipeline"), 0o700)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Release(); err != nil {
		t.Fatal(err)
	}
	lease, published, err := MaterializePipeline(t.Context(), dir, CompileOptions{Environment: env, Diagnostics: io.Discard})
	if err != nil {
		t.Fatalf("cached binary required Go: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if published || lease.key() != key || lease.entry.binaryPath() != seed.entry.binaryPath() {
		t.Fatalf("cached binary replaced: published=%t key=%s", published, lease.key())
	}
	writeCacheConfig(t, "cache:\n  max_bytes: lots\n")
	if _, _, err := MaterializePipeline(t.Context(), dir, CompileOptions{Environment: env, Diagnostics: io.Discard}); err == nil || !strings.Contains(err.Error(), "cache.max_bytes") {
		t.Fatalf("invalid ceiling not refused on cache hit: %v", err)
	}
	writeCacheConfig(t, "cache:\n  max_bytes: 0\n")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main(){println(2)}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := MaterializePipeline(t.Context(), dir, CompileOptions{Environment: env, Diagnostics: io.Discard}); err == nil {
		t.Fatal("edited source reused a stale executable without Go")
	}
}

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
			binary, err := lease.Install(filepath.Join(t.TempDir(), "verified"))
			if err != nil {
				t.Fatal(err)
			}
			result, err := exec.CommandContext(t.Context(), binary.Path()).CombinedOutput()
			if err != nil || string(result) != "workspace pipeline" {
				t.Fatalf("materialized workspace pipeline: %q %v", result, err)
			}
		})
	}
}
