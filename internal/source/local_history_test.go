package source_test

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestCommittedCheckoutHistoryAndTagsHaveLocalRemoteParity(t *testing.T) {
	for _, test := range []struct {
		name          string
		policy        nodeproto.SourceSpec
		commits, tags string
	}{
		{"default", nodeproto.SourceSpec{}, "1", ""},
		{"shallow without tags", nodeproto.SourceSpec{Depth: new(1), Tags: new(false)}, "1", ""},
		{"depth two", nodeproto.SourceSpec{Depth: new(2)}, "2", ""},
		{"full history with tags", nodeproto.SourceSpec{FullHistory: new(true), Tags: new(true)}, "3", "v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner, _ := sourcetest.Repository(t)
			for _, name := range []string{"second", "third"} {
				sourcetest.Write(t, filepath.Join(owner, name), name)
				sourcetest.Git(t, owner, "add", name)
				sourcetest.Git(t, owner, "commit", "-qm", name)
			}
			sourcetest.Git(t, owner, "tag", "v1", "HEAD~1")
			sha := sourcetest.Git(t, owner, "rev-parse", "HEAD")
			remote := "https://8.8.8.8/team/repo"
			sourcetest.Remote(t, map[string]string{remote: owner})
			for _, local := range []bool{false, true} {
				spec := source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: sha, DefaultBranch: "trunk", CommittedCheckout: true, Checkout: runspec.CheckoutPolicy{Policy: test.policy}}
				if local {
					spec.RemoteURL, spec.LocalCheckout = "", owner
				}
				credential := source.Credential{Kind: "https", Host: "8.8.8.8", Username: "source", Secret: "private"}
				entry, err := source.PrepareCheckout(t.Context(), spec, source.Options{Workspace: t.TempDir(), Access: &access{credential: credential}, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
				if err != nil {
					t.Fatal(local, err)
				}
				checkout := entry.CheckoutDirectory()
				commits := sourcetest.Git(t, checkout, "rev-list", "--count", "HEAD")
				tags := sourcetest.Git(t, checkout, "tag", "--list")
				if err := entry.Close(); err != nil {
					t.Fatal(err)
				}
				if commits != test.commits || tags != test.tags {
					t.Fatalf("local=%t: commits=%s tags=%q, want %s %q", local, commits, tags, test.commits, test.tags)
				}
			}
			if shallow := sourcetest.Git(t, owner, "rev-parse", "--is-shallow-repository"); shallow != "false" {
				t.Fatal("acquisition changed the owner's repository")
			}
			if tags := sourcetest.Git(t, owner, "tag", "--list"); tags != "v1" {
				t.Fatalf("owner tags=%q", tags)
			}
		})
	}
}
