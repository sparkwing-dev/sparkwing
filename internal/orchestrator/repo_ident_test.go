package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepoShortName_FindsGitToplevelBasename(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "myproject")
	nested := filepath.Join(repo, "sub", "deep")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := repoShortName(repo); got != "myproject" {
		t.Errorf("at toplevel: got %q, want myproject", got)
	}
	if got := repoShortName(nested); got != "myproject" {
		t.Errorf("nested: got %q, want myproject", got)
	}
}

func TestRepoShortName_GitFileMarksLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "linked-wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := repoShortName(wt); got != "linked-wt" {
		t.Errorf("worktree .git file: got %q, want linked-wt", got)
	}
}

func TestRepoShortName_EmptyOutsideAnyRepo(t *testing.T) {
	if got := repoShortName(t.TempDir()); got != "" {
		t.Errorf("outside a repo: got %q, want empty", got)
	}
}
