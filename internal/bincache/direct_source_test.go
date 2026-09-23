package bincache

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSHA1 = "0123456789abcdef0123456789abcdef01234567"

// httpOnly fetches from the plain-http test server with no address check, since it listens on loopback.
var httpOnly = directOptions{protocols: "http"}

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
		"query":              "https://github.com/o/r.git?ref=x",
		"fragment":           "https://github.com/o/r.git#main",
		"scp query":          "git@github.com:o/r.git?x",
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
	if err := directCheckout(ctx, root, remote, "main", oldSHA, first, httpOnly); err != nil {
		t.Fatalf("directCheckout old: %v", err)
	}
	if got := readMarker(t, first); got != "v1" {
		t.Fatalf("checkout at %s reads marker %q, want v1", oldSHA, got)
	}
	if head := gitOut(t, first, "rev-parse", "HEAD"); head != oldSHA {
		t.Fatalf("HEAD = %s, want %s", head, oldSHA)
	}

	second := filepath.Join(t.TempDir(), "run-2")
	if err := directCheckout(ctx, root, remote, "main", tipSHA, second, httpOnly); err != nil {
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
	if err := directCheckout(ctx, root, remote, "main", oldSHA, third, httpOnly); err != nil {
		t.Fatalf("directCheckout after a run directory was removed: %v", err)
	}
}

func TestDirectCheckoutTakesTheBranchTipWithoutACommit(t *testing.T) {
	remote, _, tipSHA := directTestRemote(t)
	dest := filepath.Join(t.TempDir(), "run")
	if err := directCheckout(context.Background(), t.TempDir(), remote, "main", "", dest, httpOnly); err != nil {
		t.Fatalf("directCheckout: %v", err)
	}
	if head := gitOut(t, dest, "rev-parse", "HEAD"); head != tipSHA {
		t.Fatalf("HEAD = %s, want the branch tip %s", head, tipSHA)
	}
}

func TestDirectCheckoutRefusesATransportOutsideTheAllowList(t *testing.T) {
	remote, oldSHA, _ := directTestRemote(t)
	dest := filepath.Join(t.TempDir(), "run")
	err := directCheckout(context.Background(), t.TempDir(), remote, "main", oldSHA, dest, directOptions{protocols: directProtocols})
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
		filepath.Join(t.TempDir(), "run"), httpOnly)
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
			err := directCheckout(context.Background(), t.TempDir(), remote, "main", sha, dest, httpOnly)
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

// fakeSSH is an ssh stand-in that appends its argv to the returned file, one
// argument per line, and fails.
func fakeSSH(t *testing.T) (program, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	program = filepath.Join(dir, "fake-ssh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + argsFile + "'\nexit 1\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return program, argsFile
}

func TestDirectCheckoutHardensTheUsersSSHCommand(t *testing.T) {
	for _, source := range []string{"GIT_SSH_COMMAND", "core.sshCommand", "GIT_SSH"} {
		t.Run(source, func(t *testing.T) {
			program, argsFile := fakeSSH(t)
			for _, name := range []string{"GIT_SSH_COMMAND", "GIT_SSH", "GIT_SSH_VARIANT"} {
				t.Setenv(name, "")
				_ = os.Unsetenv(name)
			}
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			wantUserArgs := true
			switch source {
			case "GIT_SSH_COMMAND":
				t.Setenv("GIT_SSH_COMMAND", program+" -i /keys/user")
			case "core.sshCommand":
				global := filepath.Join(t.TempDir(), "gitconfig")
				writeTestFile(t, global, "[core]\n\tsshCommand = "+program+" -i /keys/user\n")
				t.Setenv("GIT_CONFIG_GLOBAL", global)
			case "GIT_SSH":
				t.Setenv("GIT_SSH", program)
				wantUserArgs = false
			}
			err := directCheckout(context.Background(), t.TempDir(), "ssh://git@example.invalid/o/r.git",
				"main", testSHA1, filepath.Join(t.TempDir(), "run"), directOptions{protocols: "ssh"})
			if err == nil {
				t.Fatal("the fake ssh cannot serve a fetch")
			}
			raw, readErr := os.ReadFile(argsFile)
			if readErr != nil {
				t.Fatalf("the user's ssh program never ran: %v (fetch: %v)", readErr, err)
			}
			args := string(raw)
			want := []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "ForwardAgent=no", "ClearAllForwardings=yes"}
			if wantUserArgs {
				want = append(want, "/keys/user")
			}
			for _, w := range want {
				if !strings.Contains(args, w+"\n") {
					t.Errorf("ssh argv lacks %q:\n%s", w, args)
				}
			}
		})
	}
}

func TestDirectSSHCommandPrefersGitsOwnOrder(t *testing.T) {
	cases := []struct {
		name       string
		env        []string
		configured string
		want       string
	}{
		{"default", nil, "", "ssh"},
		{"env over config", []string{"GIT_SSH_COMMAND=ssh -i a", "GIT_SSH=/bin/b"}, "ssh -i c", "ssh -i a"},
		{"config over GIT_SSH", []string{"GIT_SSH=/bin/b"}, "ssh -i c", "ssh -i c"},
		{"GIT_SSH is one quoted word", []string{"GIT_SSH=/opt/it's ssh"}, "", `'/opt/it'\''s ssh'`},
	}
	for _, tc := range cases {
		if got := directSSHCommand(tc.env, tc.configured); got != tc.want+directSSHOptions {
			t.Errorf("%s: directSSHCommand = %q, want %q", tc.name, got, tc.want+directSSHOptions)
		}
	}
}

func TestDirectCheckoutDoesNotFollowARedirect(t *testing.T) {
	remote, oldSHA, _ := directTestRemote(t)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := remote + strings.TrimPrefix(r.URL.Path, "/moved")
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
	}))
	t.Cleanup(redirect.Close)

	dest := filepath.Join(t.TempDir(), "run")
	err := directCheckout(context.Background(), t.TempDir(), redirect.URL+"/moved", "main", oldSHA, dest, httpOnly)
	if err == nil {
		t.Fatal("the fetch followed a redirect to another server")
	}
}

func TestFetchPipelineSourceDirectRefusesAHostThatResolvesInward(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	opts := defaultDirectOptions()
	opts.lookup = func(_ context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("10.0.0.5")}}, nil
	}
	for _, remote := range []string{"https://git.example.com/o/r.git", "git@git.example.com:o/r.git"} {
		_, err := fetchPipelineSourceDirect(context.Background(), remote, "main", testSHA1,
			filepath.Join(t.TempDir(), "run"), opts)
		if err == nil || !strings.Contains(err.Error(), "resolves to 10.0.0.5") {
			t.Fatalf("fetch of %s: err = %v, want a refusal naming the private address", remote, err)
		}
	}
}

func TestDirectMirrorPathSharesOneMirrorAcrossSpellings(t *testing.T) {
	root := t.TempDir()
	for _, group := range [][]string{
		{"https://github.com/o/r.git", "https://GitHub.COM/o/r", "https://github.com/o/r/", "https://github.com/o/r.git/"},
		{"git@github.com:o/r.git", "git@GITHUB.com:o/r", "git@github.com:o/r/"},
		{"ssh://git@github.com/o/r.git", "ssh://git@GitHub.com/o/r"},
	} {
		want := directMirrorPath(root, group[0])
		for _, remote := range group[1:] {
			if got := directMirrorPath(root, remote); got != want {
				t.Errorf("directMirrorPath(%q) = %s, want the mirror of %q", remote, got, group[0])
			}
		}
	}
	distinct := []string{
		"https://github.com/o/r.git",
		"https://github.com/o/R.git",
		"https://github.com/o/r2.git",
		"git@github.com:o/r.git",
		"deploy@github.com:o/r.git",
		"ssh://git@github.com:2222/o/r.git",
	}
	seen := map[string]string{}
	for _, remote := range distinct {
		path := directMirrorPath(root, remote)
		if other, ok := seen[path]; ok {
			t.Errorf("%q and %q share mirror %s", remote, other, path)
		}
		seen[path] = remote
	}
}

func TestDirectCheckoutGivesUpOnAFetchThatHangs(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(release) })

	opts := httpOnly
	opts.fetchTimeout = 300 * time.Millisecond
	err := directCheckout(context.Background(), t.TempDir(), hang.URL+"/o/r.git", "main", testSHA1,
		filepath.Join(t.TempDir(), "run"), opts)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a fetch timeout", err)
	}
}

func sparkwingTree(t *testing.T) func(string) {
	return func(work string) { writeTestFile(t, filepath.Join(work, ".sparkwing", "marker"), "v1") }
}

// checkoutAndRelease checks remote out and removes the run directory, as a
// finished run does, leaving only the mirror behind.
func checkoutAndRelease(t *testing.T, root, remote, sha string, opts directOptions) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "run")
	if err := directCheckout(context.Background(), root, remote, "main", sha, dest, opts); err != nil {
		t.Fatalf("directCheckout %s: %v", remote, err)
	}
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
}

func setLastUse(t *testing.T, mirror string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(mirror, when, when); err != nil {
		t.Fatal(err)
	}
}

func mirrorExists(root, remote string) bool {
	_, err := os.Stat(filepath.Join(directMirrorPath(root, remote), "HEAD"))
	return err == nil
}

func TestDirectCheckoutEvictsTheLeastRecentlyUsedMirror(t *testing.T) {
	root := t.TempDir()
	opts := httpOnly
	opts.maxMirrors = 2
	a, shaA := directTestRepo(t, sparkwingTree(t))
	b, shaB := directTestRepo(t, sparkwingTree(t))
	c, shaC := directTestRepo(t, sparkwingTree(t))
	checkoutAndRelease(t, root, a, shaA, opts)
	checkoutAndRelease(t, root, b, shaB, opts)
	setLastUse(t, directMirrorPath(root, a), 2*time.Hour)
	setLastUse(t, directMirrorPath(root, b), time.Hour)

	checkoutAndRelease(t, root, c, shaC, opts)
	if mirrorExists(root, a) || !mirrorExists(root, b) || !mirrorExists(root, c) {
		t.Fatalf("mirrors a=%v b=%v c=%v, want the oldest (a) evicted",
			mirrorExists(root, a), mirrorExists(root, b), mirrorExists(root, c))
	}
}

func TestDirectCheckoutNeverEvictsAMirrorInUse(t *testing.T) {
	root := t.TempDir()
	opts := httpOnly
	opts.maxMirrors = 1
	live, shaLive := directTestRepo(t, sparkwingTree(t))
	locked, shaLocked := directTestRepo(t, sparkwingTree(t))
	next, shaNext := directTestRepo(t, sparkwingTree(t))

	if err := directCheckout(context.Background(), root, live, "main", shaLive,
		filepath.Join(t.TempDir(), "run"), opts); err != nil {
		t.Fatal(err)
	}
	checkoutAndRelease(t, root, locked, shaLocked, opts)
	lock, err := os.OpenFile(directMirrorPath(root, locked)+".lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if _, err := cacheLock(lock, cacheLockShared); err != nil {
		t.Fatal(err)
	}

	checkoutAndRelease(t, root, next, shaNext, opts)
	if !mirrorExists(root, live) || !mirrorExists(root, locked) {
		t.Fatalf("mirrors live=%v locked=%v, want both kept though over the cap",
			mirrorExists(root, live), mirrorExists(root, locked))
	}
}

func TestDirectCheckoutRemovesAMirrorOverTheSizeCap(t *testing.T) {
	root := t.TempDir()
	opts := httpOnly
	opts.maxMirrorBytes = 1
	remote, sha := directTestRepo(t, sparkwingTree(t))
	dest := filepath.Join(t.TempDir(), "run")
	err := directCheckout(context.Background(), root, remote, "main", sha, dest, opts)
	if err == nil || !strings.Contains(err.Error(), "over the") {
		t.Fatalf("err = %v, want a size-cap refusal", err)
	}
	if mirrorExists(root, remote) {
		t.Fatal("the oversized mirror was kept")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("an oversized fetch still produced a checkout")
	}
}

func TestDirectCheckoutEvictsOlderMirrorsPastTheTotalSize(t *testing.T) {
	root := t.TempDir()
	a, shaA := directTestRepo(t, sparkwingTree(t))
	b, shaB := directTestRepo(t, sparkwingTree(t))
	checkoutAndRelease(t, root, a, shaA, httpOnly)
	setLastUse(t, directMirrorPath(root, a), time.Hour)
	size, err := directDirSize(directMirrorPath(root, a))
	if err != nil {
		t.Fatal(err)
	}

	opts := httpOnly
	opts.maxMirrorBytes = size * 3 / 2
	checkoutAndRelease(t, root, b, shaB, opts)
	if mirrorExists(root, a) || !mirrorExists(root, b) {
		t.Fatalf("mirrors a=%v b=%v, want a evicted to fit b under the total", mirrorExists(root, a), mirrorExists(root, b))
	}
}
