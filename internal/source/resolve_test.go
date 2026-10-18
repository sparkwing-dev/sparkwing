package source_test

import (
	"context"
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
	remote := "https://8.8.8.8/team/repo"
	sourcetest.Remote(t, map[string]string{remote: repo})
	for _, local := range []bool{true, false} {
		for _, ref := range []string{"HEAD", "trunk", "refs/heads/trunk", "release", "refs/tags/release"} {
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
