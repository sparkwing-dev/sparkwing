package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestMergeQueueTemplateGitWorkflow(t *testing.T) {
	for _, scenario := range []string{"success", "failed-check", "changed-head", "dirty-check", "changed-submission", "conflict", "remote-moved"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			origin, candidate := filepath.Join(root, "origin"), filepath.Join(root, "candidate")
			gitIn(t, root, "init", "--bare", "--initial-branch=trunk", origin)
			gitIn(t, root, "clone", origin, candidate)
			write := func(path, body string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(candidate, "file"), "base\n")
			check := "true\n"
			switch scenario {
			case "failed-check":
				check = "exit 7\n"
			case "changed-head":
				check = "git -c commit.gpgsign=false commit --allow-empty -m changed\n"
			case "dirty-check":
				check = "echo dirty >> file\n"
			case "remote-moved":
				check = "git --git-dir=../origin update-ref refs/heads/trunk $(git rev-parse HEAD^)\ngit --git-dir=../origin -c user.name=t -c user.email=t@example.test commit-tree HEAD^{tree} -p HEAD -m moved | xargs git --git-dir=../origin update-ref refs/heads/trunk\n"
			}
			write(filepath.Join(candidate, "bin", "check.sh"), check)
			gitIn(t, candidate, "add", "file", "bin/check.sh")
			gitIn(t, candidate, "-c", "commit.gpgsign=false", "commit", "-m", "base")
			gitIn(t, candidate, "push", "origin", "trunk")
			gitIn(t, candidate, "checkout", "-b", "work")
			write(filepath.Join(candidate, "file"), "candidate\n")
			gitIn(t, candidate, "add", "file")
			gitIn(t, candidate, "-c", "commit.gpgsign=false", "commit", "-m", "candidate")
			git := func(dir string, args ...string) string {
				t.Helper()
				cmd := procenv.ControlCommand(os.Environ(), "git", append([]string{"-C", dir}, args...)...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git: %v: %s", err, out)
				}
				return strings.TrimSpace(string(out))
			}
			submitted := git(candidate, "rev-parse", "HEAD")
			if scenario == "changed-submission" {
				submitted = "old-head"
			}
			if scenario == "conflict" || scenario == "success" {
				other := filepath.Join(root, "other")
				gitIn(t, root, "clone", origin, other)
				file := "other"
				if scenario == "conflict" {
					file = "file"
				}
				write(filepath.Join(other, file), "upstream\n")
				gitIn(t, other, "add", file)
				gitIn(t, other, "-c", "commit.gpgsign=false", "commit", "-m", "upstream")
				gitIn(t, other, "push", "origin", "trunk")
			}
			before := git(origin, "rev-parse", "trunk")
			cmd := procenv.ControlCommand(os.Environ(), "bash", "-c", mergeQueueScript)
			cmd.Dir = candidate
			cmd.Env = append(os.Environ(), "SUBMITTED="+submitted, "BRIEF=Test intent; $(exit 99)", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
			out, err := cmd.CombinedOutput()
			if scenario == "success" {
				if err != nil {
					t.Fatalf("queue: %v\n%s", err, out)
				}
				if git(origin, "rev-parse", "trunk") != git(candidate, "rev-parse", "HEAD") {
					t.Fatal("did not push checked HEAD")
				}
				if _, err := os.Stat(filepath.Join(candidate, "other")); err != nil {
					t.Fatal("did not merge default branch")
				}
			} else {
				if err == nil {
					t.Fatalf("unsafe landing succeeded: %s", out)
				}
				if scenario != "remote-moved" && git(origin, "rev-parse", "trunk") != before {
					t.Fatal("failed entry changed default branch")
				}
				if scenario == "conflict" {
					if git(candidate, "status", "--porcelain") != "" {
						t.Fatal("conflict was not aborted")
					}
				}
			}
		})
	}
}
