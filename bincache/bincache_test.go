package bincache

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newPipelineDir creates a minimal .sparkwing-style pipeline module
// with no local replaces, so PipelineCacheKey is driven entirely by
// files added on top.
func newPipelineDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/pipeline\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	return dir
}

func mustKey(t *testing.T, dir string) string {
	t.Helper()
	k, err := PipelineCacheKey(dir)
	if err != nil {
		t.Fatalf("PipelineCacheKey: %v", err)
	}
	return k
}

func TestPipelineCacheKey_UnchangedWithoutOverlay(t *testing.T) {
	dir := newPipelineDir(t)
	first := mustKey(t, dir)
	second := mustKey(t, dir)
	if first != second {
		t.Fatalf("key should be stable without overlay: %s vs %s", first, second)
	}
	if len(first) != 17 || first[8] != '-' {
		t.Fatalf("unexpected key format: %q", first)
	}
}

func TestPipelineCacheKey_InvalidatesOnOverlayChange(t *testing.T) {
	dir := newPipelineDir(t)
	overlay := filepath.Join(dir, ".resolved.mod")

	if err := os.WriteFile(overlay, []byte("module overlay\n\nrequire foo/sparks v1.0.0\n"), 0o644); err != nil {
		t.Fatalf("write overlay A: %v", err)
	}
	keyA := mustKey(t, dir)

	if err := os.WriteFile(overlay, []byte("module overlay\n\nrequire foo/sparks v1.1.0\n"), 0o644); err != nil {
		t.Fatalf("write overlay B: %v", err)
	}
	keyB := mustKey(t, dir)

	if keyA == keyB {
		t.Fatalf("key should change when .resolved.mod changes; got %s twice", keyA)
	}
}

func TestPipelineCacheKey_IncludesOverlaySum(t *testing.T) {
	dir := newPipelineDir(t)
	sum := filepath.Join(dir, ".resolved.sum")

	if err := os.WriteFile(sum, []byte("foo/sparks v1.0.0 h1:aaaa\n"), 0o644); err != nil {
		t.Fatalf("write sum A: %v", err)
	}
	keyA := mustKey(t, dir)

	if err := os.WriteFile(sum, []byte("foo/sparks v1.0.0 h1:bbbb\n"), 0o644); err != nil {
		t.Fatalf("write sum B: %v", err)
	}
	keyB := mustKey(t, dir)

	if keyA == keyB {
		t.Fatalf("key should change when .resolved.sum changes; got %s twice", keyA)
	}
}

// installFakeGo drops a shell script named `go` on PATH that records
// its argv into the file at log. Returns the log path.
func installFakeGo(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	log := filepath.Join(binDir, "argv.log")
	// Honors `-o <dest>` by creating an empty file there.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  if [ \"$1\" = \"-o\" ]; then\n" +
		"    shift\n" +
		"    : > \"$1\"\n" +
		"    break\n" +
		"  fi\n" +
		"  shift\n" +
		"done\n" +
		"exit 0\n"
	bin := filepath.Join(binDir, "go")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestCompilePipeline_NoOverlay_PlainGoBuild(t *testing.T) {
	log := installFakeGo(t)
	dir := newPipelineDir(t)
	dest := filepath.Join(t.TempDir(), "bin", "pipelines")
	if err := CompilePipeline(dir, dest); err != nil {
		t.Fatalf("CompilePipeline: %v", err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	got := strings.TrimSpace(string(raw))
	if strings.Contains(got, "-modfile=") {
		t.Fatalf("expected plain `go build` without -modfile, got: %q", got)
	}
	if !strings.Contains(got, "build") || !strings.Contains(got, "-o") {
		t.Fatalf("expected `build ... -o ...`, got: %q", got)
	}
}

func TestCompilePipeline_WithOverlay_UsesModfile(t *testing.T) {
	log := installFakeGo(t)
	dir := newPipelineDir(t)
	overlay := filepath.Join(dir, ".resolved.mod")
	if err := os.WriteFile(overlay, []byte("module overlay\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "bin", "pipelines")
	if err := CompilePipeline(dir, dest); err != nil {
		t.Fatalf("CompilePipeline: %v", err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	got := strings.TrimSpace(string(raw))
	want := "-modfile=" + overlay
	if !strings.Contains(got, want) {
		t.Fatalf("expected %q in args, got: %q", want, got)
	}
}

// installFailingGo drops a fake `go` on PATH that prints `stderrLine`
// to stderr, `stdoutLine` to stdout, and exits with status 1. Lets
// us exercise the failure path of CompilePipeline without depending
// on a real toolchain.
func installFailingGo(t *testing.T, stderrLine, stdoutLine string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' " + shQuote(stdoutLine) + "\n" +
		"printf '%s\\n' " + shQuote(stderrLine) + " 1>&2\n" +
		"exit 1\n"
	bin := filepath.Join(binDir, "go")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// A failed compile must surface the toolchain's stdout + stderr via
// *CompileError so the trigger loop can ship them into the run's
// structured logs (instead of operators having to `kubectl logs` the
// warm-runner pod).
func TestCompilePipeline_FailureCapturesStdoutAndStderr(t *testing.T) {
	const wantStderr = "go: go.mod requires go >= 9.99.0"
	const wantStdout = "./pipeline.go:7:2: undefined: Foo"
	installFailingGo(t, wantStderr, wantStdout)
	dir := newPipelineDir(t)
	dest := filepath.Join(t.TempDir(), "bin", "pipelines")

	err := CompilePipeline(dir, dest)
	if err == nil {
		t.Fatal("expected CompilePipeline to fail")
	}
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	out := string(ce.Output)
	if !strings.Contains(out, wantStderr) {
		t.Errorf("captured output missing stderr line %q:\n%s", wantStderr, out)
	}
	if !strings.Contains(out, wantStdout) {
		t.Errorf("captured output missing stdout line %q:\n%s", wantStdout, out)
	}
	// The wrapper string stays terse so existing log lines don't
	// suddenly explode in volume.
	if !strings.HasPrefix(err.Error(), "compile .sparkwing/:") {
		t.Errorf("expected terse wrapper prefix, got: %q", err.Error())
	}
}

func TestPipelineCacheKey_IgnoresMissingOverlay(t *testing.T) {
	dir := newPipelineDir(t)
	keyA := mustKey(t, dir)
	keyB := mustKey(t, dir)
	if keyA != keyB {
		t.Fatalf("key should be stable when overlays absent: %s vs %s", keyA, keyB)
	}

	// .resolved.sum without .resolved.mod must still work.
	if err := os.WriteFile(filepath.Join(dir, ".resolved.sum"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write lone sum: %v", err)
	}
	if _, err := PipelineCacheKey(dir); err != nil {
		t.Fatalf("lone .resolved.sum should not error: %v", err)
	}
}
