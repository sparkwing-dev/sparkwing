package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScaffoldIgnoresPipelineBinaryWithoutChangingRootGitignore(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "gitignore")
	if err := os.WriteFile(outside, []byte("keep-me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	sparkwingDir := filepath.Join(repo, ".sparkwing")
	if err := bootstrapDotSparkwingOpts(repo, sparkwingDir, true); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "check-ignore", "-q", "--no-index", ".sparkwing/sparkwing-pipeline").CombinedOutput(); err != nil {
		t.Fatalf("pipeline binary is not ignored: %v: %s", err, out)
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "keep-me\n" {
		t.Fatalf("root .gitignore target changed: %q, %v", body, err)
	}
}

func TestScaffoldPreservesExistingLocalGitignore(t *testing.T) {
	repo := t.TempDir()
	sparkwingDir := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(sparkwingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ignore := filepath.Join(sparkwingDir, ".gitignore")
	if err := os.WriteFile(ignore, []byte("go.sum\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := bootstrapDotSparkwingOpts(repo, sparkwingDir, true); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(ignore)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "go.sum\nsparkwing-pipeline\n" {
		t.Fatalf("local .gitignore = %q", body)
	}
}

func TestScaffoldWarnsAboutExternalLocalGitignoreSymlink(t *testing.T) {
	repo := t.TempDir()
	sparkwingDir := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(sparkwingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "gitignore")
	if err := os.WriteFile(outside, []byte("keep-me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(sparkwingDir, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	warning := captureStderr(t, func() {
		if err := bootstrapDotSparkwingOpts(repo, sparkwingDir, true); err != nil {
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
