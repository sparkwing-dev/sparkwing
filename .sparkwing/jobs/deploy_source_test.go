package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDeployShallowMainProofUsesTheExactCandidate(t *testing.T) {
	for _, main := range []string{"tip", "advanced", "removed"} {
		t.Run(main, func(t *testing.T) {
			f := newFakeDeploy(t)
			origin := deployGitIn(t, f.repo, "remote", "get-url", "origin")
			shallow := filepath.Join(t.TempDir(), "shallow")
			deployGitIn(t, f.repo, "clone", "--quiet", "--depth", "1", "file://"+origin, shallow)
			want := f.head
			switch main {
			case "advanced":
				want = advanceDeployMain(t, f)
			case "removed":
				want = f.old.Commit
				deployGitIn(t, origin, "update-ref", "refs/heads/main", want)
			}
			f.repo = shallow
			deployGitIn(t, shallow, "fetch", "--quiet", "origin", f.head)
			before, err := os.ReadFile(filepath.Join(shallow, ".git", "FETCH_HEAD"))
			if err != nil {
				t.Fatal(err)
			}
			err = f.deployer(false).requireMainAncestor(t.Context(), f.head)
			if (err != nil) != (main == "removed") {
				t.Fatalf("main %s, candidate %s: %v", want, f.head, err)
			}
			if got := deployGitIn(t, shallow, "rev-parse", "refs/remotes/origin/main"); got != want {
				t.Fatalf("checked stale main %s, want %s", got, want)
			}
			after, err := os.ReadFile(filepath.Join(shallow, ".git", "FETCH_HEAD"))
			if err != nil || string(after) != string(before) {
				t.Fatalf("source proof changed: %q -> %q, %v", before, after, err)
			}
			if got := deployGitIn(t, shallow, "rev-parse", "HEAD"); got != f.head {
				t.Fatalf("candidate tree moved to %s, want %s", got, f.head)
			}
			if main == "tip" && deployGitIn(t, shallow, "rev-parse", "--is-shallow-repository") != "true" {
				t.Fatal("the exact main tip fetched older source history")
			}
		})
	}
}

func TestDeploySourceAncestryFetchesMissingHistory(t *testing.T) {
	for _, commits := range []int{2, 70} {
		t.Run(fmt.Sprint(commits), func(t *testing.T) {
			f := newFakeDeploy(t)
			for range 70 {
				deployGitIn(t, f.repo, "commit", "--quiet", "--allow-empty", "-m", "release history")
			}
			ancestor := deployGitIn(t, f.repo, "rev-parse", "HEAD")
			for range commits {
				deployGitIn(t, f.repo, "commit", "--quiet", "--allow-empty", "-m", "advance")
			}
			f.head = deployGitIn(t, f.repo, "rev-parse", "HEAD")
			deployGitIn(t, f.repo, "push", "--quiet", "origin", "HEAD:main")
			origin := deployGitIn(t, f.repo, "remote", "get-url", "origin")
			shallow := filepath.Join(t.TempDir(), "shallow")
			deployGitIn(t, f.repo, "clone", "--quiet", "--depth", "1", "file://"+origin, shallow)
			f.repo = shallow
			if err := f.deployer(false).requireSourceAncestor(t.Context(), ancestor, f.head); err != nil {
				t.Fatal(err)
			}
			if got := deployGitIn(t, shallow, "rev-parse", "--is-shallow-repository"); (got == "true") != (commits < 64) {
				t.Fatalf("history depth after %d missing commits: shallow=%s", commits, got)
			}
			if got := deployGitIn(t, shallow, "rev-parse", "HEAD"); got != f.head {
				t.Fatalf("candidate tree moved to %s, want %s", got, f.head)
			}
		})
	}
}
