package bincache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testSHA1 = "0123456789abcdef0123456789abcdef01234567"

func TestValidateDirectSourceRefusesUnsafeRemotes(t *testing.T) {
	cases := map[string]string{
		"file scheme":        "file:///tmp/repo.git",
		"local path":         "/tmp/repo.git",
		"relative path":      "../repo.git",
		"ext transport":      "ext::sh -c touch% /tmp/pwned",
		"ext without space":  "ext::nc%20host%2022",
		"http":               "http://github.com/o/r.git",
		"git protocol":       "git://github.com/o/r.git",
		"leading dash":       "-uupload-pack=touch /tmp/pwned",
		"scp leading dash":   "--upload-pack=x@github.com:o/r.git",
		"https userinfo":     "https://user@github.com/o/r.git",
		"https password":     "https://x-access-token:ghs_secret@github.com/o/r.git",
		"ssh password":       "ssh://git:secret@github.com/o/r.git",
		"loopback":           "https://127.0.0.1/o/r.git",
		"scp path dash":      "git@github.com:-oProxyCommand=x",
		"empty":              "",
		"hostless scp-alike": "github.com:o/r.git",
	}
	for name, remote := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ValidateDirectSource(remote, testSHA1); err == nil {
				t.Fatalf("ValidateDirectSource(%q) accepted it", remote)
			}
		})
	}
}

func TestValidateDirectSourceAcceptsHTTPSAndSSH(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/sparkwing-dev/sparkwing.git",
		"git@github.com:sparkwing-dev/sparkwing.git",
		"ssh://git@github.com/sparkwing-dev/sparkwing.git",
	} {
		got, sha, err := ValidateDirectSource(remote, strings.ToUpper(testSHA1))
		if err != nil {
			t.Fatalf("ValidateDirectSource(%q): %v", remote, err)
		}
		if got != remote || sha != testSHA1 {
			t.Fatalf("ValidateDirectSource(%q) = %q, %q", remote, got, sha)
		}
	}
}

func TestValidateDirectSourceRefusesAnythingButAFullCommitID(t *testing.T) {
	for _, sha := range []string{
		"abc123",
		testSHA1[:39],
		testSHA1 + "0",
		strings.Repeat("a", 63),
		"--upload-pack=x" + strings.Repeat("a", 25),
		"g123456789abcdef0123456789abcdef01234567",
		"HEAD",
		"refs/heads/main",
	} {
		if _, _, err := ValidateDirectSource("https://github.com/o/r.git", sha); err == nil {
			t.Fatalf("ValidateDirectSource accepted commit %q", sha)
		}
	}
	if _, _, err := ValidateDirectSource("https://github.com/o/r.git", strings.Repeat("b", 64)); err != nil {
		t.Fatalf("a sha256 commit id: %v", err)
	}
}

func TestFetchPipelineSourceDirectRefusesABranchThatIsAnOption(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	_, err := FetchPipelineSourceDirect(context.Background(), "https://github.com/o/r.git",
		"--upload-pack=touch", "", filepath.Join(t.TempDir(), "run"))
	if err == nil || !strings.Contains(err.Error(), "not a branch name") {
		t.Fatalf("err = %v, want a branch-name refusal", err)
	}
}

func TestDirectFetchURLUsesHTTPSForGitHubWithoutAnSSHIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")
	cases := map[string]string{
		"git@github.com:o/r.git":       "https://github.com/o/r.git",
		"git@github.com:o/r":           "https://github.com/o/r.git",
		"ssh://git@github.com/o/r.git": "https://github.com/o/r.git",
		"git@gitlab.com:o/r.git":       "git@gitlab.com:o/r.git",
		"https://github.com/o/r.git":   "https://github.com/o/r.git",
		"git@github.com:o/r/extra.git": "git@github.com:o/r/extra.git",
	}
	for in, want := range cases {
		if got := DirectFetchURL(in); got != want {
			t.Errorf("DirectFetchURL(%q) = %q, want %q", in, got, want)
		}
	}

	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	if got := DirectFetchURL("git@github.com:o/r.git"); got != "git@github.com:o/r.git" {
		t.Fatalf("with an ssh agent, DirectFetchURL rewrote the remote to %q", got)
	}
}

// directTestRemote serves a bare repository over the smart http transport,
// which is the https code path without a certificate.
func directTestRemote(t *testing.T) (remote, oldSHA, tipSHA string) {
	t.Helper()
	repoParent := t.TempDir()
	oldSHA, tipSHA = makeBareRepoWithSparkwing(t, repoParent, "widgets", "main")
	srv := startGitcacheTestServer(t, repoParent)
	t.Cleanup(srv.Close)
	return srv.URL + "/git/widgets.git", oldSHA, tipSHA
}

func TestDirectCheckoutFetchesTheExactCommitAndReusesTheMirror(t *testing.T) {
	remote, oldSHA, tipSHA := directTestRemote(t)
	root := t.TempDir()
	ctx := context.Background()

	first := filepath.Join(t.TempDir(), "run-1")
	if err := directCheckout(ctx, root, remote, "main", oldSHA, first, "http"); err != nil {
		t.Fatalf("directCheckout old: %v", err)
	}
	if got := readMarker(t, first); got != "v1" {
		t.Fatalf("checkout at %s reads marker %q, want v1", oldSHA, got)
	}
	if head := gitOut(t, first, "rev-parse", "HEAD"); head != oldSHA {
		t.Fatalf("HEAD = %s, want %s", head, oldSHA)
	}

	second := filepath.Join(t.TempDir(), "run-2")
	if err := directCheckout(ctx, root, remote, "main", tipSHA, second, "http"); err != nil {
		t.Fatalf("directCheckout tip: %v", err)
	}
	if got := readMarker(t, second); got != "v2" {
		t.Fatalf("checkout at %s reads marker %q, want v2", tipSHA, got)
	}

	mirrors, _ := filepath.Glob(filepath.Join(root, "*.git"))
	if len(mirrors) != 1 {
		t.Fatalf("mirrors = %v, want one shared by both runs", mirrors)
	}

	// safety: a removed run directory must not block the next checkout of the same commit.
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	third := filepath.Join(t.TempDir(), "run-3")
	if err := directCheckout(ctx, root, remote, "main", oldSHA, third, "http"); err != nil {
		t.Fatalf("directCheckout after a run directory was removed: %v", err)
	}
}

func TestDirectCheckoutTakesTheBranchTipWithoutACommit(t *testing.T) {
	remote, _, tipSHA := directTestRemote(t)
	dest := filepath.Join(t.TempDir(), "run")
	if err := directCheckout(context.Background(), t.TempDir(), remote, "main", "", dest, "http"); err != nil {
		t.Fatalf("directCheckout: %v", err)
	}
	if head := gitOut(t, dest, "rev-parse", "HEAD"); head != tipSHA {
		t.Fatalf("HEAD = %s, want the branch tip %s", head, tipSHA)
	}
}

func TestDirectCheckoutRefusesATransportOutsideTheAllowList(t *testing.T) {
	remote, oldSHA, _ := directTestRemote(t)
	dest := filepath.Join(t.TempDir(), "run")
	err := directCheckout(context.Background(), t.TempDir(), remote, "main", oldSHA, dest, directProtocols)
	if err == nil {
		t.Fatal("an http remote was fetched although only https and ssh are allowed")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("refused fetch left a checkout at %s", dest)
	}
}

func TestDirectCheckoutRefusesACommitTheRemoteDoesNotHave(t *testing.T) {
	remote, _, _ := directTestRemote(t)
	err := directCheckout(context.Background(), t.TempDir(), remote, "main", testSHA1,
		filepath.Join(t.TempDir(), "run"), "http")
	if err == nil || !strings.Contains(err.Error(), "is the commit pushed?") {
		t.Fatalf("err = %v, want a fetch failure that asks whether the commit was pushed", err)
	}
}

func readMarker(t *testing.T, checkout string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(checkout, ".sparkwing", "marker"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// directTestRepo serves a one-commit repository whose tree populate writes,
// built without the ambient git config so a test's hostile config reaches only
// the code under test.
func directTestRepo(t *testing.T, populate func(work string)) (remote, sha string) {
	t.Helper()
	repoParent := t.TempDir()
	work := filepath.Join(t.TempDir(), "work")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	git(work, "init", "--quiet", "--initial-branch=main")
	populate(work)
	git(work, "add", "-A")
	git(work, "commit", "--quiet", "-m", "tree")
	sha = git(work, "rev-parse", "HEAD")
	bare := filepath.Join(repoParent, "repo.git")
	git("", "clone", "--bare", "--quiet", work, bare)
	git(bare, "config", "uploadpack.allowReachableSHA1InWant", "true")
	srv := startGitcacheTestServer(t, repoParent)
	t.Cleanup(srv.Close)
	return srv.URL + "/git/repo.git", sha
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDirectCheckoutRunsNoFilterDriverTheTreeNames(t *testing.T) {
	cases := map[string]string{
		"smudge":     "[filter \"evil\"]\n\tsmudge = touch %s; cat\n",
		"process":    "[filter \"evil\"]\n\tprocess = touch %s\n\trequired = true\n",
		"lfs smudge": "[filter \"lfs\"]\n\tsmudge = touch %s; cat\n\trequired = true\n",
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			driver := "evil"
			if strings.Contains(config, "lfs") {
				driver = "lfs"
			}
			remote, sha := directTestRepo(t, func(work string) {
				writeTestFile(t, filepath.Join(work, ".gitattributes"), "* filter="+driver+"\n")
				writeTestFile(t, filepath.Join(work, ".sparkwing", "marker"), "v1")
			})
			marker := filepath.Join(t.TempDir(), "filter-ran")
			global := filepath.Join(t.TempDir(), "gitconfig")
			writeTestFile(t, global, strings.ReplaceAll(config, "%s", marker))
			t.Setenv("GIT_CONFIG_GLOBAL", global)

			dest := filepath.Join(t.TempDir(), "run")
			err := directCheckout(context.Background(), t.TempDir(), remote, "main", sha, dest, "http")
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("the checkout ran the %s filter from the ambient config", driver)
			}
			if err != nil {
				t.Fatalf("directCheckout: %v", err)
			}
			if got := readMarker(t, dest); got != "v1" {
				t.Fatalf("marker = %q, want v1", got)
			}
		})
	}
}
