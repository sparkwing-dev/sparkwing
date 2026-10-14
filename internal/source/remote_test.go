package source_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func remoteFixture(t *testing.T, repositories map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable shim; Windows source support requires Windows CI")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "commands.jsonl")
	encode := func(value any) string { b, _ := json.Marshal(value); return string(b) }
	program := "#!" + python + "\n" + `import json, os, stat, sys
args = sys.argv[1:]
config = [x.split('=', 1)[1] for x in args if x.startswith('include.path=')]
record = {'args': args, 'env': dict(os.environ), 'files': config}
for name in config:
    if stat.S_IMODE(os.stat(name).st_mode) != 0o600:
        raise RuntimeError('credential file is not private')
with open(` + encode(log) + `, 'a') as out:
    out.write(json.dumps(record) + '\n')
mapping = ` + encode(repositories) + `
if 'fetch' in args or 'ls-remote' in args:
    args = [mapping.get(x, x) for x in args]
    os.environ['GIT_ALLOW_PROTOCOL'] = 'file'
os.execv(` + encode(realGit) + `, [` + encode(realGit) + `] + args)
`
	name := filepath.Join(bin, "git")
	if err := os.WriteFile(name, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestPrepareCheckoutFetchesSealedCommitAfterHeadAdvances(t *testing.T) {
	dir, sealed := repository(t)
	write(t, filepath.Join(dir, ".sparkwing", "pipeline.go"), "package main // later\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "later")
	remote := "https://8.8.8.8/team/repo"
	log := remoteFixture(t, map[string]string{remote: dir})
	t.Setenv("SPARKWING_API_TOKEN", "ambient-private-token")
	t.Setenv("GIT_TRACE", filepath.Join(t.TempDir(), "trace"))
	workspace := t.TempDir()
	a := &access{credential: source.Credential{Kind: "https", Host: "8.8.8.8", Username: "source", Secret: "private-source-password"}}
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: sealed}, source.Options{GitEnvironment: gitBase(t), Workspace: workspace, Access: a})
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
	dir, _ := repository(t)
	remote := "https://8.8.8.8/team/repo"
	remoteFixture(t, map[string]string{remote: dir})
	workspace := t.TempDir()
	_, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: strings.Repeat("a", 40)}, source.Options{GitEnvironment: gitBase(t), Workspace: workspace, Access: &access{}})
	if err == nil {
		t.Fatal("accepted a commit absent from the source")
	}
	remaining, readErr := os.ReadDir(workspace)
	if readErr != nil || len(remaining) != 0 {
		t.Fatalf("failed checkout left %v, %v", remaining, readErr)
	}
}

func TestPrepareCheckoutChecksSubmoduleAllowlistBeforeFetching(t *testing.T) {
	child, childSHA := repository(t)
	parent, _ := repository(t)
	remote := "https://8.8.8.8/team/repo"
	subremote := "https://8.8.8.8/team/library"
	write(t, filepath.Join(parent, ".gitmodules"), "[submodule \"library\"]\n\tpath = library\n\turl = ../library\n")
	git(t, parent, "add", ".gitmodules")
	git(t, parent, "update-index", "--add", "--cacheinfo", "160000,"+childSHA+",library")
	git(t, parent, "commit", "-qm", "submodule")
	sha := git(t, parent, "rev-parse", "HEAD")
	log := remoteFixture(t, map[string]string{remote: parent, subremote: child})
	spec := source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: sha, DefaultBranch: "trunk", Checkout: runspec.CheckoutPolicy{Policy: nodeproto.SourceSpec{Submodules: new(true)}}}
	credential := source.Credential{Kind: "https", Host: "8.8.8.8", Username: "source", Secret: "private-source-password"}
	if _, err := source.PrepareCheckout(t.Context(), spec, source.Options{GitEnvironment: gitBase(t), Workspace: t.TempDir(), Access: &access{credential: credential}}); err == nil {
		t.Fatal("fetched an unauthorized submodule")
	}
	commands, err := os.ReadFile(log)
	if err != nil || strings.Contains(string(commands), `"fetch", "--quiet", "--no-recurse-submodules", "--no-tags", "--depth=1", "--", "`+subremote+`"`) {
		t.Fatalf("unauthorized fetch: %s, %v", commands, err)
	}
	credential.ExtraRepositories = []string{"team/library"}
	entry, err := source.PrepareCheckout(t.Context(), spec, source.Options{GitEnvironment: gitBase(t), Workspace: t.TempDir(), Access: &access{credential: credential}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = entry.Close() })
	if _, err := os.Stat(filepath.Join(filepath.Dir(entry.Directory), "library", ".sparkwing", "pipeline.go")); err != nil {
		t.Fatalf("authorized submodule not checked out: %v", err)
	}
}

func TestPrepareCheckoutAppliesHistoryAndTagPolicy(t *testing.T) {
	dir, _ := repository(t)
	git(t, dir, "tag", "v1")
	write(t, filepath.Join(dir, "later"), "later")
	git(t, dir, "add", "later")
	git(t, dir, "commit", "-qm", "later")
	sha := git(t, dir, "rev-parse", "HEAD")
	remote := "https://8.8.8.8/team/repo"
	remoteFixture(t, map[string]string{remote: dir})
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
			entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: sha, DefaultBranch: "trunk", Checkout: runspec.CheckoutPolicy{Policy: test.policy}}, source.Options{GitEnvironment: gitBase(t), Workspace: t.TempDir(), Access: &access{}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = entry.Close() })
			if got := git(t, entry.Directory, "rev-list", "--count", "HEAD"); got != test.commits {
				t.Fatalf("commit count = %s, want %s", got, test.commits)
			}
			if got := git(t, entry.Directory, "tag", "--list"); got != test.tags {
				t.Fatalf("tags = %s, want %s", got, test.tags)
			}
		})
	}
}

func TestPrepareCheckoutOwnerHelperFailureDoesNotFallBackToAnonymous(t *testing.T) {
	dir, sha := repository(t)
	remote := "https://8.8.8.8/team/repo"
	log := remoteFixture(t, map[string]string{remote: dir})
	ownerHome := t.TempDir()
	write(t, filepath.Join(ownerHome, ".gitconfig"), "[credential]\n\thelper = !exit 1\n")
	_, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: sha}, source.Options{GitEnvironment: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + ownerHome}, Workspace: t.TempDir(), Access: &access{}})
	if err == nil || !strings.Contains(err.Error(), "helper failed") {
		t.Fatalf("helper failure = %v", err)
	}
	commands, readErr := os.ReadFile(log)
	if readErr != nil || strings.Contains(string(commands), `"fetch"`) {
		t.Fatalf("anonymous fetch after failed helper: %s, %v", commands, readErr)
	}
}
