package bincache

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func newGitPipelineDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := newPipelineDir(t)
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v: %s", err, out)
	}
	return dir
}

func TestPipelineCacheKey_SkipsGitignoredFiles(t *testing.T) {
	dir := newGitPipelineDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\n*.tfplan\n")
	before := mustKey(t, dir)

	writeFile(t, filepath.Join(dir, "dist/binary"), "several megabytes, pretend")
	writeFile(t, filepath.Join(dir, "local.tfplan"), "provider state")

	if after := mustKey(t, dir); after != before {
		t.Fatalf("gitignored files must not affect the key: %s -> %s", before, after)
	}
}

func TestPipelineCacheKey_InvalidatesOnTrackedFileEdit(t *testing.T) {
	dir := newGitPipelineDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\n")
	before := mustKey(t, dir)

	writeFile(t, filepath.Join(dir, "jobs.go"), "package main\n\nfunc extra() {}\n")

	if after := mustKey(t, dir); after == before {
		t.Fatalf("a tracked source file must move the key, got %s twice", before)
	}
}

func TestPipelineCacheKey_InvalidatesOnGitignoreEdit(t *testing.T) {
	dir := newGitPipelineDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\n")
	writeFile(t, filepath.Join(dir, "generated.go"), "package main\n")
	before := mustKey(t, dir)

	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\ngenerated.go\n")

	if after := mustKey(t, dir); after == before {
		t.Fatalf("newly ignoring a hashed file must move the key, got %s twice", before)
	}
}

func TestPipelineCacheKey_HashAllFilesEnvRestoresFullHashing(t *testing.T) {
	dir := newGitPipelineDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\n")
	t.Setenv(HashAllFilesEnv, "1")
	before := mustKey(t, dir)

	writeFile(t, filepath.Join(dir, "dist/binary"), "pretend output")

	if after := mustKey(t, dir); after == before {
		t.Fatalf("%s must restore hashing of ignored files, got %s twice", HashAllFilesEnv, before)
	}
}

func TestPipelineCacheKey_NonRepoDirHashesEverything(t *testing.T) {
	dir := newPipelineDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\n")
	before := mustKey(t, dir)

	writeFile(t, filepath.Join(dir, "dist/binary"), "pretend output")

	if after := mustKey(t, dir); after == before {
		t.Fatalf("outside a repository nothing is ignorable, got %s twice", before)
	}
}
