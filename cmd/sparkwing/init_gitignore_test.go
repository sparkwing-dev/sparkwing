package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func enterScaffoldTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return repo
}

func checkPipelineBinaryIgnored(t *testing.T, repo string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", repo, "check-ignore", "-v", ".sparkwing/sparkwing-pipeline").CombinedOutput(); err != nil {
		t.Fatalf("pipeline binary is not ignored: %v: %s", err, out)
	} else if !strings.Contains(string(out), ".sparkwing/.gitignore:") {
		t.Fatalf("pipeline binary ignored by wrong source: %s", out)
	}
}

func TestPipelineNewIgnoresBinaryWithoutChangingRootGitignore(t *testing.T) {
	repo := enterScaffoldTestRepo(t)
	outside := filepath.Join(t.TempDir(), "gitignore")
	if err := os.WriteFile(outside, []byte("keep-me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	if err := runPipelineNew([]string{"--name", "sample", "--template", "minimal"}); err != nil {
		t.Fatal(err)
	}
	checkPipelineBinaryIgnored(t, repo)
	if body, err := os.ReadFile(outside); err != nil || string(body) != "keep-me\n" {
		t.Fatalf("root .gitignore target changed: %q, %v", body, err)
	}
}

func TestExistingScaffoldUpdatesLocalIgnoreAndWarnsAboutExternalSymlink(t *testing.T) {
	repo := enterScaffoldTestRepo(t)
	if err := runPipelineNew([]string{"--name", "first", "--template", "minimal"}); err != nil {
		t.Fatal(err)
	}
	ignore := filepath.Join(repo, ".sparkwing", ".gitignore")
	if err := os.WriteFile(ignore, []byte("go.sum\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runPipelineNew([]string{"--name", "middle", "--template", "minimal"}); err != nil {
		t.Fatal(err)
	}
	checkPipelineBinaryIgnored(t, repo)
	if body, err := os.ReadFile(ignore); err != nil || string(body) != "go.sum\nsparkwing-pipeline\n" {
		t.Fatalf("local .gitignore = %q, %v", body, err)
	}

	if err := os.Remove(ignore); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "gitignore")
	if err := os.WriteFile(outside, []byte("keep-me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, ignore); err != nil {
		t.Fatal(err)
	}
	warning := captureStderr(t, func() {
		if err := runPipelineNew([]string{"--name", "second", "--template", "minimal"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(warning, "add sparkwing-pipeline to an ignore file") {
		t.Fatalf("missing ignore warning: %q", warning)
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "keep-me\n" {
		t.Fatalf("external .gitignore changed: %q, %v", body, err)
	}
}
