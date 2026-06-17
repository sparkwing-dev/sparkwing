package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderHookScript_BlockingHooksAbortOnFailure(t *testing.T) {
	for _, hook := range []string{"pre-commit", "pre-push"} {
		script := renderHookScript(hook, []string{"lint", "test"})
		if !strings.Contains(script, "set -e") {
			t.Errorf("%s hook should set -e so git aborts on failure:\n%s", hook, script)
		}
		if !strings.Contains(script, "sparkwing run lint\n") {
			t.Errorf("%s hook should invoke each pipeline plainly:\n%s", hook, script)
		}
		if strings.Contains(script, "|| true") {
			t.Errorf("%s hook must not swallow pipeline failures:\n%s", hook, script)
		}
	}
}

func TestRenderHookScript_QuietByDefault(t *testing.T) {
	for _, hook := range []string{"pre-commit", "pre-push", "post-commit"} {
		script := renderHookScript(hook, []string{"lint"})
		if !strings.Contains(script, `export SPARKWING_LOG_FORMAT="${SPARKWING_LOG_FORMAT:-quiet}"`) {
			t.Errorf("%s hook should default the log format to quiet while honoring an override:\n%s", hook, script)
		}
	}
}

func TestRenderHookScript_PostCommitIsNonBlocking(t *testing.T) {
	script := renderHookScript("post-commit", []string{"self-install", "notify"})
	if strings.Contains(script, "set -e") {
		t.Errorf("post-commit hook must not set -e (the commit already landed):\n%s", script)
	}
	for _, p := range []string{"self-install", "notify"} {
		if !strings.Contains(script, "sparkwing run "+p+" || true\n") {
			t.Errorf("post-commit hook should tolerate %q failing and continue:\n%s", p, script)
		}
	}
	if !strings.HasSuffix(script, "exit 0\n") {
		t.Errorf("post-commit hook must always exit zero:\n%s", script)
	}
}

func TestHooksInstall_WritesPostCommitHook(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, filepath.Join(repo, ".sparkwing", "sparkwing.yaml"), `pipelines:
  - name: lint
    entrypoint: Lint
    on:
      pre_commit: {}
  - name: self-install
    entrypoint: SelfInstall
    on:
      post_commit: {}
`)
	if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runHooksInstall([]string{"--repo", repo}); err != nil {
		t.Fatalf("install: %v", err)
	}

	post := readRepoFile(t, filepath.Join(repo, ".git", "hooks", "post-commit"))
	if !strings.Contains(post, sparkwingHookMarker) {
		t.Errorf("post-commit hook missing managed marker:\n%s", post)
	}
	if !strings.Contains(post, "sparkwing run self-install || true") {
		t.Errorf("post-commit hook should invoke its pipeline non-blocking:\n%s", post)
	}
	if strings.Contains(post, "set -e") {
		t.Errorf("post-commit hook must be non-blocking:\n%s", post)
	}

	pre := readRepoFile(t, filepath.Join(repo, ".git", "hooks", "pre-commit"))
	if !strings.Contains(pre, "set -e") || !strings.Contains(pre, "sparkwing run lint") {
		t.Errorf("pre-commit hook should stay blocking:\n%s", pre)
	}

	out := captureStdout(t, func() {
		if err := runHooksStatus([]string{"--repo", repo}); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "post-commit -> self-install\n") {
		t.Errorf("status should show the clean pipeline name without the || true suffix:\n%s", out)
	}
}

func writeRepoFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
