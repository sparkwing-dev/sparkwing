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

func TestCompilePipeline_WithOverlayAndGoWork_SkipsModfile(t *testing.T) {
	log := installFakeGo(t)
	t.Setenv("GOWORK", "")
	dir := newPipelineDir(t)
	overlay := filepath.Join(dir, ".resolved.mod")
	if err := os.WriteFile(overlay, []byte("module overlay\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	work := filepath.Join(dir, "go.work")
	if err := os.WriteFile(work, []byte("go 1.26\nuse .\n"), 0o644); err != nil {
		t.Fatalf("write go.work: %v", err)
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
	if strings.Contains(got, "-modfile=") {
		t.Fatalf("expected -modfile to be omitted when go.work is present, got: %q", got)
	}
}

func TestCompilePipeline_WithGoWorkAndGoworkOff_UsesModfile(t *testing.T) {
	log := installFakeGo(t)
	t.Setenv("GOWORK", "off")
	dir := newPipelineDir(t)
	overlay := filepath.Join(dir, ".resolved.mod")
	if err := os.WriteFile(overlay, []byte("module overlay\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	work := filepath.Join(dir, "go.work")
	if err := os.WriteFile(work, []byte("go 1.26\nuse .\n"), 0o644); err != nil {
		t.Fatalf("write go.work: %v", err)
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
	if !strings.Contains(got, "-modfile="+overlay) {
		t.Fatalf("expected -modfile to be honored when GOWORK=off, got: %q", got)
	}
}

// installFakeGoLoggingEnv drops a fake `go` on PATH that records its argv
// and the GOWORK it ran under, so a test can assert both the build flags
// and whether the enclosing workspace was disabled. Returns the log path.
func installFakeGoLoggingEnv(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	log := filepath.Join(binDir, "argv.log")
	script := "#!/bin/sh\n" +
		"printf 'ARGV %s\\n' \"$*\" >> " + log + "\n" +
		"printf 'GOWORK %s\\n' \"${GOWORK}\" >> " + log + "\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  if [ \"$1\" = \"-o\" ]; then\n" +
		"    shift\n" +
		"    : > \"$1\"\n" +
		"    break\n" +
		"  fi\n" +
		"  shift\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "go"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestCompilePipeline_NonCoveringGoWork_IgnoredAndModfileHonored(t *testing.T) {
	log := installFakeGoLoggingEnv(t)
	t.Setenv("GOWORK", "")
	root := t.TempDir()
	pipelineDir := filepath.Join(root, "project", ".sparkwing")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		t.Fatalf("mkdir pipeline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pipelineDir, "go.mod"), []byte("module example.com/pipeline\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pipelineDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	overlay := filepath.Join(pipelineDir, ".resolved.mod")
	if err := os.WriteFile(overlay, []byte("module overlay\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.26\n\nuse ./other\n"), 0o644); err != nil {
		t.Fatalf("write go.work: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "bin", "pipelines")
	if err := CompilePipeline(pipelineDir, dest); err != nil {
		t.Fatalf("CompilePipeline: %v", err)
	}
	got := strings.TrimSpace(string(mustReadFile(t, log)))
	if !strings.Contains(got, "-modfile="+overlay) {
		t.Errorf("expected -modfile honored when the enclosing go.work does not cover the module, got:\n%s", got)
	}
	if !strings.Contains(got, "GOWORK off") {
		t.Errorf("expected the non-covering workspace disabled via GOWORK=off, got:\n%s", got)
	}
}

func TestCompilePipeline_CoveringGoWork_Honored(t *testing.T) {
	log := installFakeGoLoggingEnv(t)
	t.Setenv("GOWORK", "")
	root := t.TempDir()
	pipelineDir := filepath.Join(root, "svc")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		t.Fatalf("mkdir pipeline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pipelineDir, "go.mod"), []byte("module example.com/pipeline\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pipelineDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	overlay := filepath.Join(pipelineDir, ".resolved.mod")
	if err := os.WriteFile(overlay, []byte("module overlay\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.26\n\nuse ./svc\n"), 0o644); err != nil {
		t.Fatalf("write go.work: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "bin", "pipelines")
	if err := CompilePipeline(pipelineDir, dest); err != nil {
		t.Fatalf("CompilePipeline: %v", err)
	}
	got := strings.TrimSpace(string(mustReadFile(t, log)))
	if strings.Contains(got, "-modfile=") {
		t.Errorf("expected -modfile skipped when the workspace covers the module, got:\n%s", got)
	}
	if strings.Contains(got, "GOWORK off") {
		t.Errorf("expected a covering workspace left in force, but it was disabled:\n%s", got)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
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
	if !strings.HasPrefix(err.Error(), "compile .sparkwing/:") {
		t.Errorf("expected terse wrapper prefix, got: %q", err.Error())
	}
}

// writeFile is a fatal-on-error os.WriteFile for test fixtures.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newWorkspaceScenario lays out a pipeline module plus a sibling local
// module linked through a covering go.work, and points GOWORK at that
// workspace. link is a go.work directive line ("replace ..." or
// "use ..."). Returns the pipeline dir and the local module dir.
func newWorkspaceScenario(t *testing.T, link string) (pipelineDir, moduleDir string) {
	t.Helper()
	root := t.TempDir()
	pipelineDir = filepath.Join(root, "svc")
	writeFile(t, filepath.Join(pipelineDir, "go.mod"), "module example.com/pipeline\n\ngo 1.22\n\nrequire example.com/tmpl v0.0.0\n")
	writeFile(t, filepath.Join(pipelineDir, "main.go"), "package main\n\nfunc main() {}\n")

	moduleDir = filepath.Join(root, "tmpl")
	writeFile(t, filepath.Join(moduleDir, "go.mod"), "module example.com/tmpl\n\ngo 1.22\n")
	writeFile(t, filepath.Join(moduleDir, "registry.go"), "package tmpl\n")
	writeFile(t, filepath.Join(moduleDir, "registry.yaml"), "count: 14\n")

	work := filepath.Join(root, "go.work")
	writeFile(t, work, "go 1.26\n\nuse ./svc\n\n"+link+"\n")
	t.Setenv("GOWORK", work)
	return pipelineDir, moduleDir
}

func TestPipelineCacheKey_InvalidatesOnGoWorkReplaceTargetEdit(t *testing.T) {
	pipelineDir, moduleDir := newWorkspaceScenario(t, "replace example.com/tmpl => ./tmpl")
	keyA := mustKey(t, pipelineDir)

	writeFile(t, filepath.Join(moduleDir, "registry.go"), "package tmpl\n\nvar Added = 1\n")
	keyB := mustKey(t, pipelineDir)
	if keyA == keyB {
		t.Fatalf("editing a go.work replace target's source must change the key; got %s twice", keyA)
	}
}

func TestPipelineCacheKey_InvalidatesOnGoWorkUseTargetEdit(t *testing.T) {
	pipelineDir, moduleDir := newWorkspaceScenario(t, "use ./tmpl")
	keyA := mustKey(t, pipelineDir)

	writeFile(t, filepath.Join(moduleDir, "registry.go"), "package tmpl\n\nvar Added = 1\n")
	keyB := mustKey(t, pipelineDir)
	if keyA == keyB {
		t.Fatalf("editing a go.work use-module's source must change the key; got %s twice", keyA)
	}
}

// Adding a template to a replaced registry lands as an embedded,
// non-Go asset; the key must still turn over.
func TestPipelineCacheKey_InvalidatesOnGoWorkTargetEmbeddedAssetEdit(t *testing.T) {
	pipelineDir, moduleDir := newWorkspaceScenario(t, "replace example.com/tmpl => ./tmpl")
	keyA := mustKey(t, pipelineDir)

	writeFile(t, filepath.Join(moduleDir, "registry.yaml"), "count: 38\n")
	keyB := mustKey(t, pipelineDir)
	if keyA == keyB {
		t.Fatalf("editing a replace target's embedded asset must change the key; got %s twice", keyA)
	}
}

func TestPipelineCacheKey_StableWhenGoWorkTargetUnchanged(t *testing.T) {
	pipelineDir, _ := newWorkspaceScenario(t, "replace example.com/tmpl => ./tmpl")
	if first, second := mustKey(t, pipelineDir), mustKey(t, pipelineDir); first != second {
		t.Fatalf("key should be stable when nothing changes: %s vs %s", first, second)
	}
}

// A workspace that does not cover the pipeline module is disabled at
// build time (GOWORK=off), so its targets must not feed the key.
func TestPipelineCacheKey_IgnoresNonCoveringGoWorkTargets(t *testing.T) {
	root := t.TempDir()
	pipelineDir := filepath.Join(root, "svc")
	writeFile(t, filepath.Join(pipelineDir, "go.mod"), "module example.com/pipeline\n\ngo 1.22\n")
	writeFile(t, filepath.Join(pipelineDir, "main.go"), "package main\n\nfunc main() {}\n")
	otherDir := filepath.Join(root, "other")
	writeFile(t, filepath.Join(otherDir, "go.mod"), "module example.com/other\n\ngo 1.22\n")
	writeFile(t, filepath.Join(otherDir, "data.go"), "package other\n")
	work := filepath.Join(root, "go.work")
	writeFile(t, work, "go 1.26\n\nuse ./other\n")
	t.Setenv("GOWORK", work)

	keyA := mustKey(t, pipelineDir)
	writeFile(t, filepath.Join(otherDir, "data.go"), "package other\n\nvar X = 1\n")
	keyB := mustKey(t, pipelineDir)
	if keyA != keyB {
		t.Fatalf("a non-covering workspace's modules must not affect the key: %s vs %s", keyA, keyB)
	}
}

// The go.mod-local replace path must also cover embedded, non-Go
// assets in the replaced module.
func TestPipelineCacheKey_InvalidatesOnGoModReplaceTargetEmbeddedAssetEdit(t *testing.T) {
	t.Setenv("GOWORK", "off")
	root := t.TempDir()
	pipelineDir := filepath.Join(root, "svc")
	moduleDir := filepath.Join(root, "tmpl")
	writeFile(t, filepath.Join(pipelineDir, "go.mod"), "module example.com/pipeline\n\ngo 1.22\n\nrequire example.com/tmpl v0.0.0\n\nreplace example.com/tmpl => ../tmpl\n")
	writeFile(t, filepath.Join(pipelineDir, "main.go"), "package main\n\nfunc main() {}\n")
	writeFile(t, filepath.Join(moduleDir, "go.mod"), "module example.com/tmpl\n\ngo 1.22\n")
	writeFile(t, filepath.Join(moduleDir, "registry.yaml"), "count: 14\n")

	keyA := mustKey(t, pipelineDir)
	writeFile(t, filepath.Join(moduleDir, "registry.yaml"), "count: 38\n")
	keyB := mustKey(t, pipelineDir)
	if keyA == keyB {
		t.Fatalf("editing a go.mod replace target's embedded asset must change the key; got %s twice", keyA)
	}
}

func TestPipelineCacheKey_IgnoresMissingOverlay(t *testing.T) {
	dir := newPipelineDir(t)
	keyA := mustKey(t, dir)
	keyB := mustKey(t, dir)
	if keyA != keyB {
		t.Fatalf("key should be stable when overlays absent: %s vs %s", keyA, keyB)
	}

	if err := os.WriteFile(filepath.Join(dir, ".resolved.sum"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write lone sum: %v", err)
	}
	if _, err := PipelineCacheKey(dir); err != nil {
		t.Fatalf("lone .resolved.sum should not error: %v", err)
	}
}
