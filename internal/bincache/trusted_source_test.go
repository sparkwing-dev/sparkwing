package bincache

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type trustedFixture struct {
	env      []string
	repos    string
	hostPort string
	url      string
	cred     DirectCredential
	head     string
}

const trustedTok = "ghs_trusted_fixture"

func newTrustedFixture(t *testing.T, subURL string) trustedFixture {
	t.Helper()
	if subURL == "" {
		subURL = "../lib.git"
	}
	f := trustedFixture{repos: t.TempDir()}
	f.env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL="+os.DevNull)
	srv, _ := authGitServer(t, f.repos, trustedTok)
	f.url = srv.URL
	f.hostPort = strings.TrimPrefix(srv.URL, "http://")
	f.cred = DirectCredential{Kind: CredentialHTTPS, Host: f.hostPort, Username: "x-access-token", Secret: trustedTok}

	lib := filepath.Join(t.TempDir(), "lib")
	gitRun(t, f.env, "init", "--quiet", "-b", "main", lib)
	writeTestFile(t, filepath.Join(lib, "lib.txt"), "lib\n")
	gitRun(t, f.env, "-C", lib, "add", ".")
	gitRun(t, f.env, "-C", lib, "commit", "--quiet", "-m", "lib")
	gitRun(t, f.env, "clone", "--quiet", "--bare", lib, filepath.Join(f.repos, "lib.git"))

	app := filepath.Join(t.TempDir(), "app")
	gitRun(t, f.env, "init", "--quiet", "-b", "main", app)
	writeTestFile(t, filepath.Join(app, "go.mod"), "module example.com/app\n\n")
	gitRun(t, f.env, "-C", app, "add", ".")
	gitRun(t, f.env, "-C", app, "commit", "--quiet", "-m", "one")
	gitRun(t, f.env, "-C", app, "tag", "v1.0.0")
	gitRun(t, f.env, "-C", app, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", lib, "lib")
	gitRun(t, f.env, "-C", app, "config", "-f", ".gitmodules", "submodule.lib.url", subURL)
	gitRun(t, f.env, "-C", app, "add", ".")
	gitRun(t, f.env, "-C", app, "commit", "--quiet", "-m", "two")
	f.head = gitRun(t, f.env, "-C", app, "rev-parse", "HEAD")
	gitRun(t, f.env, "clone", "--quiet", "--bare", app, filepath.Join(f.repos, "app.git"))
	return f
}

func (f trustedFixture) git(t *testing.T, cred DirectCredential) *trustedGit {
	t.Helper()
	g, err := openTrustedGit(f.url+"/app.git", cred, httpOnly)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.close() })
	return g
}

// The checkout is a real repository with the pinned commit, origin names the
// remote, and the credential that fetched it is nowhere in the tree.
func TestTrustedCheckoutDeliversARepositoryWithoutTheCredential(t *testing.T) {
	f := newTrustedFixture(t, "../lib.git")
	dest := filepath.Join(t.TempDir(), "src")
	if err := f.git(t, f.cred).checkout(context.Background(), f.head, "main", dest, SourceOptions{Depth: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dest, ".git")); err != nil || !fi.IsDir() {
		t.Fatalf(".git is not a directory: %v", err)
	}
	if got := gitRun(t, f.env, "-C", dest, "rev-parse", "HEAD"); got != f.head {
		t.Fatalf("HEAD = %s, want %s", got, f.head)
	}
	if got := gitRun(t, f.env, "-C", dest, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("branch = %s, want main", got)
	}
	if got := gitRun(t, f.env, "-C", dest, "remote", "get-url", "origin"); got != f.url+"/app.git" {
		t.Fatalf("origin = %s", got)
	}
	if got := gitRun(t, f.env, "-C", dest, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("depth 1 fetched %s commits", got)
	}
	if got := gitRun(t, f.env, "-C", dest, "tag"); got != "" {
		t.Fatalf("tags were fetched without being asked for: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "lib", "lib.txt")); err == nil {
		t.Fatal("a submodule was checked out without being asked for")
	}
	assertNoSecret(t, dest, trustedTok)
}

func TestTrustedCheckoutFetchesFullHistoryTagsAndSubmodules(t *testing.T) {
	f := newTrustedFixture(t, "../lib.git")
	dest := filepath.Join(t.TempDir(), "src")
	o := SourceOptions{Tags: true, Submodules: true}
	if err := f.git(t, f.cred).checkout(context.Background(), f.head, "", dest, o, []string{f.hostPort + "/lib"}); err != nil {
		t.Fatal(err)
	}
	if got := gitRun(t, f.env, "-C", dest, "rev-list", "--count", "HEAD"); got != "2" {
		t.Fatalf("full history fetched %s commits, want 2", got)
	}
	if got := gitRun(t, f.env, "-C", dest, "tag"); got != "v1.0.0" {
		t.Fatalf("tags = %q", got)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "lib", "lib.txt")); err != nil || string(got) != "lib\n" {
		t.Fatalf("submodule not checked out: %q, %v", got, err)
	}
	assertNoSecret(t, dest, trustedTok)
}

// Negative controls: a wrong token fetches nothing and is not echoed, a
// submodule on another host is refused before it is fetched, LFS is refused
// for a credential other than the GitHub App's, and a negative depth is refused.
func TestTrustedCheckoutRefusals(t *testing.T) {
	f := newTrustedFixture(t, "../lib.git")
	wrong := f.cred
	wrong.Secret = "ghs_wrong_one"
	err := f.git(t, wrong).checkout(context.Background(), f.head, "", filepath.Join(t.TempDir(), "a"), SourceOptions{Depth: 1}, nil)
	if err == nil || strings.Contains(err.Error(), "ghs_wrong_one") {
		t.Fatalf("wrong token: err = %v", err)
	}

	for name, sub := range map[string]func(hostPort string) string{
		"unlisted, relative":   func(string) string { return "../secret.git" },
		"unlisted, absolute":   func(hp string) string { return "http://" + hp + "/secret.git" },
		"climbs past the host": func(string) string { return "../../../evil.example/lib.git" },
		"another host":         func(string) string { return "https://elsewhere.example/lib.git" },
	} {
		mal := newTrustedFixture(t, "")
		mal.setSubmoduleURL(t, sub(mal.hostPort))
		err := mal.git(t, mal.cred).checkout(context.Background(), mal.head, "", filepath.Join(t.TempDir(), "b"),
			SourceOptions{Depth: 1, Submodules: true}, []string{mal.hostPort + "/lib"})
		if err == nil || !strings.Contains(err.Error(), "not a repository the team's owner listed") {
			t.Fatalf("%s: err = %v, want the listed-repository refusal", name, err)
		}
	}
	if err := f.git(t, f.cred).checkout(context.Background(), f.head, "", filepath.Join(t.TempDir(), "c"),
		SourceOptions{Depth: 1, Submodules: true}, nil); err == nil {
		t.Fatal("a submodule was fetched with no repository listed")
	}

	for name, o := range map[string]SourceOptions{"lfs": {LFS: true}, "depth": {Depth: -1}} {
		if err := f.git(t, f.cred).checkout(context.Background(), f.head, "", filepath.Join(t.TempDir(), name), o, nil); err == nil {
			t.Fatalf("%s was not refused", name)
		}
	}
}

func assertNoSecret(t *testing.T, root, secret string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		body, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(body), secret) {
			t.Errorf("%s holds the credential", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *trustedFixture) setSubmoduleURL(t *testing.T, url string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	gitRun(t, f.env, "clone", "--quiet", filepath.Join(f.repos, "app.git"), work)
	gitRun(t, f.env, "-C", work, "config", "-f", ".gitmodules", "submodule.lib.url", url)
	gitRun(t, f.env, "-C", work, "commit", "--quiet", "-am", "point the submodule elsewhere")
	gitRun(t, f.env, "-C", work, "push", "--quiet", "origin", "HEAD:main")
	f.head = gitRun(t, f.env, "-C", work, "rev-parse", "HEAD")
}
