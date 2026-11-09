//go:build !windows

package bincache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failedCompileWriter struct{}

func (failedCompileWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCompilePipelineUsesCapturedToolAndDiagnostics(t *testing.T) {
	bin := t.TempDir()
	tool := filepath.Join(bin, "go")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf 'captured Go output\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	options := CompileOptions{Environment: []string{"PATH=" + bin, "GOWORK=off"}}
	var out bytes.Buffer
	options.Diagnostics = &out
	if err := CompilePipeline(t.Context(), dir, filepath.Join(t.TempDir(), "pipeline"), options); err != nil || out.String() != "captured Go output\n" {
		t.Fatalf("captured compiler: error=%v output=%q", err, &out)
	}
	options.Diagnostics = failedCompileWriter{}
	if err := CompilePipeline(t.Context(), dir, filepath.Join(t.TempDir(), "pipeline"), options); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("lost compiler diagnostic error: %v", err)
	}
}

func TestGoCommandsRequireExplicitInputs(t *testing.T) {
	for _, options := range []CompileOptions{{}, {Environment: os.Environ()}, {Diagnostics: io.Discard}} {
		if err := CompilePipeline(t.Context(), t.TempDir(), "unused", options); err == nil {
			t.Fatal("compiler inferred missing inputs")
		}
		if err := runGo(t.Context(), t.TempDir(), nil, options); err == nil {
			t.Fatal("Go command inferred missing inputs")
		}
		if _, _, err := MaterializePipeline(t.Context(), t.TempDir(), options); err == nil {
			t.Fatal("materialization inferred missing inputs")
		}
	}
	options := CompileOptions{Environment: []string{"PATH=" + t.TempDir()}, Diagnostics: io.Discard}
	if err := CompilePipeline(t.Context(), t.TempDir(), "unused", options); err == nil || !strings.Contains(err.Error(), "Go 1.26+") {
		t.Fatalf("missing captured compiler: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	options.Environment = os.Environ()
	if err := CompilePipeline(ctx, t.TempDir(), "unused", options); !errors.Is(err, context.Canceled) {
		t.Fatalf("compiler ignored cancellation: %v", err)
	}
}

func TestCacheKeyFollowsCompilersNotPath(t *testing.T) {
	dir := newPipelineDir(t)
	bin := t.TempDir()
	tool := filepath.Join(bin, "go")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"+fakeGoEnvProbe), 0o700); err != nil {
		t.Fatal(err)
	}
	key := func(path string) string {
		t.Helper()
		k, _, err := ExplainCacheKey(dir, []string{"PATH=" + path, "GOWORK=off"})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	first := key(bin)
	if other := key(t.TempDir() + string(filepath.ListSeparator) + bin); other != first {
		t.Fatal("a PATH entry without a compiler changed the pipeline cache key")
	}
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"+fakeGoEnvProbe+"# upgraded\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if other := key(bin); other == first {
		t.Fatal("replacing go behind an unchanged PATH kept the pipeline cache key")
	}
}

func TestPipelineBuildsWithoutCgoUnlessTheEnvironmentAsks(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\n"+fakeGoEnvProbe+"if [ \"$1\" = build ]; then printf 'CGO_ENABLED=%s\\n' \"$CGO_ENABLED\"; fi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := newPipelineDir(t)
	build := func(extra ...string) (string, string) {
		t.Helper()
		env := append([]string{"PATH=" + bin, "GOWORK=off"}, extra...)
		var out bytes.Buffer
		if err := CompilePipeline(t.Context(), dir, filepath.Join(t.TempDir(), "pipeline"), CompileOptions{Environment: env, Diagnostics: &out}); err != nil {
			t.Fatal(err)
		}
		key, _, err := ExplainCacheKey(dir, env)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out.String()), key
	}
	unset, unsetKey := build()
	off, offKey := build("CGO_ENABLED=0")
	on, onKey := build("CGO_ENABLED=1")
	if unset != "CGO_ENABLED=0" || off != "CGO_ENABLED=0" || on != "CGO_ENABLED=1" {
		t.Fatalf("compiler saw unset=%q off=%q on=%q", unset, off, on)
	}
	if unsetKey != offKey || onKey == offKey {
		t.Fatal("pipeline cache key does not follow the effective CGO_ENABLED")
	}
}

func TestCacheKeyUsesSelectedToolchainPatchAndBuildSettings(t *testing.T) {
	dir, bin := newPipelineDir(t), t.TempDir()
	t.Setenv("SPARKWING_HOME", t.TempDir())
	settings := filepath.Join(t.TempDir(), "effective.json")
	marker := filepath.Join(t.TempDir(), "build")
	script := "#!/bin/sh\nif [ \"$1 $2\" = 'env -json' ]; then\n" +
		"while IFS= read -r line; do printf '%s\\n' \"$line\"; done < " + shQuote(settings) + "\nexit 0\nfi\n" + fakeGoEnvProbe +
		"if [ \"$1\" = build ]; then printf rebuild > " + shQuote(marker) + "; exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin, "GOWORK=off"}
	base := map[string]string{"GOVERSION": "go1.26.6", "GOTOOLCHAIN": "auto", "GOAMD64": "v1", "GOARM64": "v8.0", "CGO_ENABLED": "0"}
	writeSettings := func(values map[string]string) {
		t.Helper()
		body, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, settings, string(body)+"\n")
	}
	writeSettings(base)
	key, _, err := ExplainCacheKey(dir, env)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := PipelineEntry(key)
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := entry.AcquireOrMaterialize(t.Context(), func(path string) error { return os.WriteFile(path, []byte("seed"), 0o700) })
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"GOVERSION": "go1.26.7", "GOTOOLCHAIN": "local", "GOAMD64": "v2", "GOARM64": "v8.1", "GOEXPERIMENT": "loopvar", "CGO_ENABLED": "1", "CGO_CFLAGS": "-O3", "CC": "other-cc"} {
		t.Run(name, func(t *testing.T) {
			changed := maps.Clone(base)
			changed[name] = value
			writeSettings(changed)
			if lease, _, err := MaterializePipeline(t.Context(), dir, CompileOptions{Environment: env, Diagnostics: io.Discard}); err == nil {
				_ = lease.Release()
				t.Fatal("changed effective setting reused the seeded binary")
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("mismatch did not reach the compiler: %v", err)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
		})
	}
}
