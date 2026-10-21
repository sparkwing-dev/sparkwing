package source_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

func TestResolveRefUsesLocalAndAuthorizedRemoteCommits(t *testing.T) {
	repo, sha := sourcetest.Repository(t)
	sourcetest.Git(t, repo, "tag", "-am", "release", "release")
	sourcetest.Git(t, repo, "update-ref", "refs/heads/main", sha)
	sourcetest.Git(t, repo, "update-ref", "refs/remotes/origin/main", sha)
	remote := "https://8.8.8.8/team/repo"
	sourcetest.Remote(t, map[string]string{remote: repo})
	for _, local := range []bool{true, false} {
		for _, ref := range []string{"HEAD", "trunk", "refs/heads/trunk", "release", "refs/tags/release", "origin/main", "refs/remotes/origin/main"} {
			t.Run(ref+"/local="+map[bool]string{true: "true", false: "false"}[local], func(t *testing.T) {
				spec := source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote}
				if local {
					spec.RemoteURL, spec.LocalCheckout = "", repo
				}
				got, err := source.ResolveRef(t.Context(), spec, ref, source.Options{Access: &access{}, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
				if err != nil || got != sha {
					t.Fatalf("resolved %s to %s: %v", ref, got, err)
				}
			})
		}
	}
}

func TestVerifiedExecutionCommitPassesThroughWithoutBranchLookup(t *testing.T) {
	sha := strings.Repeat("a", 40)
	git, code, err := source.ResolveRevisions(t.Context(), runspec.Git{Ref: "refs/heads/main", SHA: sha}, runspec.Source{}, "", func(string) (string, error) { t.Fatal("verified commit caused a branch lookup"); return "", nil })
	if err != nil || git.SHA != sha || code.Ref != sha {
		t.Fatal(git, code, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := source.ResolveRevisions(ctx, git, code, "", nil); err != context.Canceled {
		t.Fatal("cancelled revision lookup succeeded", err)
	}
}

func TestResolveRefRefusesDashLedReference(t *testing.T) {
	repo, _ := sourcetest.Repository(t)
	marker := filepath.Join(t.TempDir(), "executed")
	for _, local := range []bool{true, false} {
		spec := source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: "https://8.8.8.8/team/repo"}
		if local {
			spec.LocalCheckout, spec.RemoteURL = repo, ""
		}
		_, err := source.ResolveRef(t.Context(), spec, "--upload-pack=touch "+marker, source.Options{Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}})
		if err == nil {
			t.Fatal("option-like ref resolved")
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("ref executed as a Git option")
		}
	}
}

func TestResolveRemoteRefWithoutLocalTrackingBranch(t *testing.T) {
	repo, sha := sourcetest.Repository(t)
	sourcetest.Git(t, repo, "branch", "feature")
	remote := "https://8.8.8.8/team/repo"
	sourcetest.Remote(t, map[string]string{remote: repo})
	got, err := source.ResolveRef(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote}, "feature", source.Options{Access: &access{}, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
	if err != nil || got != sha {
		t.Fatalf("remote-only branch resolved to %q: %v", got, err)
	}
}

func TestResolveRefRefusesAmbiguousNamesAndCommitPrefixes(t *testing.T) {
	repo, first := sourcetest.Repository(t)
	sourcetest.Git(t, repo, "tag", "main", first)
	sourcetest.Git(t, repo, "commit", "--allow-empty", "-m", "next")
	second := sourcetest.Git(t, repo, "rev-parse", "HEAD")
	sourcetest.Git(t, repo, "branch", "main", second)
	remote := "https://8.8.8.8/team/repo"
	sourcetest.Remote(t, map[string]string{remote: repo})
	for _, local := range []bool{true, false} {
		spec := source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote}
		if local {
			spec.RemoteURL, spec.LocalCheckout = "", repo
		}
		options := source.Options{Access: &access{}, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment}
		for _, ref := range []string{"main", first[:12], "refs/heads/main~1", "main^", "HEAD@{0}"} {
			if sha, err := source.ResolveRef(t.Context(), spec, ref, options); err == nil {
				t.Errorf("local=%v ref=%s resolved to %s", local, ref, sha)
			}
		}
		for ref, want := range map[string]string{"refs/heads/main": second, "refs/tags/main": first} {
			if sha, err := source.ResolveRef(t.Context(), spec, ref, options); err != nil || sha != want {
				t.Errorf("local=%v ref=%s resolved to %s: %v", local, ref, sha, err)
			}
		}
	}
}
