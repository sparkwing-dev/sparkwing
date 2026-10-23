package source_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestCommittedCheckoutPolicyHasLocalRemoteParity(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, submodules := range []bool{false, true} {
			for _, lfs := range []bool{false, true} {
				t.Run(fmt.Sprintf("local=%t/submodules=%t/lfs=%t", local, submodules, lfs), func(t *testing.T) {
					child, childSHA := sourcetest.Repository(t)
					parent, _ := sourcetest.Repository(t)
					remote, childRemote := "https://8.8.8.8/team/repo", "https://8.8.8.8/team/library"
					sourcetest.Git(t, parent, "remote", "add", "origin", remote)
					sourcetest.Write(t, filepath.Join(parent, ".gitmodules"), "[submodule \"library\"]\n path = library\n url = ../library\n")
					sourcetest.Write(t, filepath.Join(parent, "lfs.bin"), "version https://git-lfs.github.com/spec/v1\n")
					sourcetest.Git(t, parent, "add", ".")
					sourcetest.Git(t, parent, "update-index", "--add", "--cacheinfo", "160000,"+childSHA+",library")
					sourcetest.Git(t, parent, "commit", "-qm", "policy inputs")
					codeSHA := sourcetest.Git(t, parent, "rev-parse", "HEAD")
					sourcetest.Write(t, filepath.Join(parent, "execution-only"), "execution")
					sourcetest.Git(t, parent, "add", "execution-only")
					sourcetest.Git(t, parent, "commit", "-qm", "execution commit")
					sha := sourcetest.Git(t, parent, "rev-parse", "HEAD")
					sourcetest.Remote(t, map[string]string{remote: parent, childRemote: child}, remote+".git/info/lfs")
					spec := source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: sha, PipelineSHA: codeSHA, DefaultBranch: "trunk", CommittedCheckout: true, Checkout: runspec.CheckoutPolicy{Policy: nodeproto.SourceSpec{Submodules: &submodules, LFS: &lfs}}}
					if local {
						spec.RemoteURL, spec.LocalCheckout = "", parent
					}
					credential := source.Credential{Kind: "https", Host: "8.8.8.8", Username: "source", Secret: "private", ExtraRepositories: []string{"team/library"}}
					entry, err := source.PrepareCheckout(t.Context(), spec, source.Options{Workspace: t.TempDir(), Access: &access{credential: credential}, Base: procenv.Base{Ambient: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := entry.Close(); err != nil {
							t.Error(err)
						}
					})
					for _, checkout := range []string{entry.CheckoutDirectory(), filepath.Dir(entry.Directory)} {
						_, err = os.Stat(filepath.Join(checkout, "library", ".sparkwing", "pipeline.go"))
						if (err == nil) != submodules {
							t.Fatalf("submodule policy=%t, error=%v", submodules, err)
						}
						body, err := os.ReadFile(filepath.Join(checkout, "lfs.bin"))
						if err != nil || strings.Contains(string(body), "downloaded LFS content") != lfs {
							t.Fatalf("LFS policy=%t, body=%q, error=%v", lfs, body, err)
						}

					}
					original, err := os.ReadFile(filepath.Join(parent, "lfs.bin"))
					if err != nil || !strings.Contains(string(original), "spec/v1") {
						t.Fatal("policy changed the owner's checkout", err)
					}
				})
			}
		}
	}
}
