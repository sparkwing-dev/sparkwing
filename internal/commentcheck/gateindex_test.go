package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/gitenv"
)

func TestScopedAdds_StagedReadsTheIndexTheCommitIsBeingBuiltIn(t *testing.T) {
	repo := newGatedRepo(t)
	gate := gateIndexOf(t, repo)
	writeSource(t, filepath.Join(repo, "narrate.go"), "package p\n\nfunc f(n int) int {\n\t// bump the counter\n\treturn n + 1\n}\n")
	gitWith(t, repo, gate, "add", "narrate.go")

	t.Run("a hook-launched check reads the gate index", func(t *testing.T) {
		t.Setenv("GIT_INDEX_FILE", "")
		t.Setenv(gitenv.GateIndexVar, gate)

		added, err := scopedAdds(repo, true, "")
		if err != nil {
			t.Fatalf("scopedAdds: %v", err)
		}
		if !added["narrate.go"][4] {
			t.Errorf("the staged diff reported %v; a comment the commit is adding is not charged to it", added)
		}
	})

	t.Run("an index the caller bound deliberately wins", func(t *testing.T) {
		caller := gateIndexOf(t, repo)
		writeSource(t, filepath.Join(repo, "other.go"), "package p\n\nfunc g() {\n\t// narrate the other file\n}\n")
		gitWith(t, repo, caller, "add", "other.go")
		t.Setenv("GIT_INDEX_FILE", caller)
		t.Setenv(gitenv.GateIndexVar, gate)

		added, err := scopedAdds(repo, true, "")
		if err != nil {
			t.Fatalf("scopedAdds: %v", err)
		}
		if _, ok := added["other.go"]; !ok {
			t.Errorf("the staged diff reported %v; a caller that hands the gate an index it built is ignored", added)
		}
	})

	t.Run("no gate index leaves the repository's own index in charge", func(t *testing.T) {
		t.Setenv("GIT_INDEX_FILE", "")
		t.Setenv(gitenv.GateIndexVar, "")

		added, err := scopedAdds(repo, true, "")
		if err != nil {
			t.Fatalf("scopedAdds: %v", err)
		}
		if len(added) != 0 {
			t.Errorf("the staged diff reported %v against a repository with nothing staged", added)
		}
	})
}

func newGatedRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeSource(t, filepath.Join(dir, "base.go"), "// Package p is the base.\npackage p\n")
	gitWith(t, dir, "", "init", "-b", "main")
	gitWith(t, dir, "", "add", "base.go")
	gitWith(t, dir, "", "commit", "-m", "base")
	return dir
}

func gateIndexOf(t *testing.T, repo string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "next-index.lock")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSource(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitWith(t *testing.T, dir, index string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=commentcheck test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=commentcheck test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	}
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
