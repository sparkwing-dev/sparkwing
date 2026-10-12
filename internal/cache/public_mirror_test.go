package cache

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

type gitOrigin struct {
	*httptest.Server
	mu      sync.Mutex
	private map[string]bool
}

func newGitOrigin(t *testing.T, root string) *gitOrigin {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	o := &gitOrigin{private: map[string]bool{}}
	backend := &cgi.Handler{
		Path: gitBin, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	o.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repo, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		o.mu.Lock()
		private := o.private[repo]
		o.mu.Unlock()
		if _, pass, _ := r.BasicAuth(); private && pass != "secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="origin"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(o.Close)
	ca := filepath.Join(t.TempDir(), "origin-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: o.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSL_CAINFO", ca)
	return o
}

func (o *gitOrigin) setPrivate(repo string, private bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.private[repo] = private
}

func commitTo(t *testing.T, work, bare, file, body string) string {
	t.Helper()
	if _, err := os.Stat(work); os.IsNotExist(err) {
		runGit(t, work, "init", "-b", "main")
		runGit(t, work, "config", "user.email", "test@example.invalid")
		runGit(t, work, "config", "user.name", "Test")
	}
	if err := os.WriteFile(filepath.Join(work, file), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", file)
	runGit(t, work, "commit", "-m", file)
	if _, err := os.Stat(bare); os.IsNotExist(err) {
		runGit(t, filepath.Dir(bare), "init", "--bare", "-b", "main", bare)
	}
	runGit(t, work, "push", bare, "HEAD:refs/heads/main")
	return strings.TrimSpace(runGit(t, work, "rev-parse", "HEAD"))
}

func fetchFromCache(t *testing.T, srv *httptest.Server, name, bearer, commit string) bool {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	cmd := exec.Command("git", "-c", "http.extraHeader=Authorization: Bearer "+bearer,
		"fetch", "--quiet", srv.URL+"/git/"+name, commit)
	cmd.Dir = dir
	if cmd.Run() != nil {
		return false
	}
	return exec.Command("git", "-C", dir, "cat-file", "-e", commit+"^{commit}").Run() == nil
}

func registerForTest(t *testing.T, name, repoURL string) {
	t.Helper()
	repoNamesMu.Lock()
	repoNames[name] = repoURL
	repoNamesMu.Unlock()
	t.Cleanup(func() {
		repoNamesMu.Lock()
		delete(repoNames, name)
		repoNamesMu.Unlock()
	})
}

// A mirror the cache filled using a credential its own environment supplies
// holds a private repository behind an https URL. Another team's grant reads
// only what origin serves without a credential: the public repository, but not
// the private one, nor a commit pushed after the public one turned private.
func TestTeamGrantsReadOnlyWhatOriginServesAnonymously(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sparkwing-cache has no Windows release, so its mirror environment targets only Linux and macOS")
	}
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	root := t.TempDir()
	repos := filepath.Join(root, "origin")
	if err := os.MkdirAll(repos, 0o755); err != nil {
		t.Fatal(err)
	}
	origin := newGitOrigin(t, repos)
	origin.setPrivate("private.git", true)
	helper := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(helper, []byte("[credential]\n\thelper = \"!f() { echo username=cache; echo password=secret; }; f\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", helper)

	privateSHA := commitTo(t, filepath.Join(root, "private-src"), filepath.Join(repos, "private.git"), "secret.txt", "private")
	publicSHA := commitTo(t, filepath.Join(root, "public-src"), filepath.Join(repos, "public.git"), "readme.txt", "public")
	privateURL, publicURL := origin.URL+"/private.git", origin.URL+"/public.git"
	// safety: registration refuses a loopback origin, so the names go in the
	// table directly, as the operator's own registration would put them.
	privateName, publicName := sourceurl.ClaimedRepoNameFromURL(privateURL), sourceurl.ClaimedRepoNameFromURL(publicURL)
	registerForTest(t, privateName, privateURL)
	registerForTest(t, publicName, publicURL)

	if out, err := cloneMirror(privateURL, filepath.Join(repoDir, repoHash(privateURL)+".git")); err != nil {
		t.Fatalf("the cache's own credential did not clone the private origin: %v\n%s", err, out)
	}
	if !fetchFromCache(t, srv, privateName, token, privateSHA) {
		t.Error("the operator could not fetch its private mirror")
	}

	grant := grantFor(t, token, "unrelated-team")
	if fetchFromCache(t, srv, privateName, grant, privateSHA) {
		t.Error("another team's grant fetched a commit the cache cloned with its own credential")
	}
	if !fetchFromCache(t, srv, publicName, grant, publicSHA) {
		t.Error("another team's grant could not fetch a public repository through the cache")
	}

	origin.setPrivate("public.git", true)
	laterSHA := commitTo(t, filepath.Join(root, "public-src"), filepath.Join(repos, "public.git"), "later.txt", "private now")
	// safety: the operator's read clones its own mirror of the now-private origin with the cache's credential.
	if !fetchFromCache(t, srv, publicName, token, laterSHA) {
		t.Fatal("the operator could not fetch the commit its credential reaches")
	}
	if fetchFromCache(t, srv, publicName, grant, laterSHA) {
		t.Error("another team's grant fetched a commit pushed after its origin turned private")
	}
}

func proxyAllTo(t *testing.T, addr string) {
	t.Helper()
	// hack: registration refuses loopback hosts, so git reaches the local origin as git.example.com through a proxy.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := net.Dial("tcp", addr)
		if r.Method != http.MethodConnect || err != nil {
			http.Error(w, "CONNECT to the origin only", http.StatusBadGateway)
			return
		}
		client, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		_, _ = io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n")
		go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close() }()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
	}))
	t.Cleanup(proxy.Close)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy"} {
		t.Setenv(k, proxy.URL)
	}
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
}

// A failed anonymous clone puts the public mirror on the clone cooldown, and
// the operator re-registering the repository lets the next team read retry it.
func TestRegistrationClearsThePublicCloneCooldown(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	root := t.TempDir()
	repos := filepath.Join(root, "origin")
	origin := newGitOrigin(t, repos)
	proxyAllTo(t, origin.Listener.Addr().String())
	sha := commitTo(t, filepath.Join(root, "src"), filepath.Join(repos, "public.git"), "readme.txt", "public")
	repoURL := "https://git.example.com/public.git"
	name := sourceurl.ClaimedRepoNameFromURL(repoURL)
	register := "/git/register?name=" + name + "&repo=" + url.QueryEscape(repoURL)
	t.Cleanup(func() {
		repoNamesMu.Lock()
		delete(repoNames, name)
		repoNamesMu.Unlock()
	})

	origin.setPrivate("public.git", true)
	if code, body := send(t, srv, http.MethodPost, register, token, ""); code != http.StatusOK {
		t.Fatalf("register = %d: %s", code, body)
	}
	grant := grantFor(t, token, "team-a")
	if fetchFromCache(t, srv, name, grant, sha) {
		t.Fatal("a team read cloned an origin that refused anonymous access")
	}
	origin.setPrivate("public.git", false)
	if fetchFromCache(t, srv, name, grant, sha) {
		t.Fatal("a team read retried the clone inside its cooldown")
	}
	if code, body := send(t, srv, http.MethodPost, register, token, ""); code != http.StatusOK {
		t.Fatalf("re-register = %d: %s", code, body)
	}
	if !fetchFromCache(t, srv, name, grant, sha) {
		t.Error("re-registering did not let a team read clone the public mirror again")
	}
}

// The keep-warm pass refreshes a public mirror a team read, from origin and
// without the credential the cache's own mirrors fetch with.
func TestKeepWarmRefreshesPublicMirrorsAnonymously(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sparkwing-cache has no Windows release, so its mirror environment targets only Linux and macOS")
	}
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	root := t.TempDir()
	repos := filepath.Join(root, "origin")
	origin := newGitOrigin(t, repos)
	helper := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(helper, []byte("[credential]\n\thelper = \"!f() { echo username=cache; echo password=secret; }; f\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", helper)
	src, bare := filepath.Join(root, "src"), filepath.Join(repos, "public.git")
	first := commitTo(t, src, bare, "readme.txt", "public")
	repoURL := origin.URL + "/public.git"
	name := sourceurl.ClaimedRepoNameFromURL(repoURL)
	registerForTest(t, name, repoURL)
	if !fetchFromCache(t, srv, name, grantFor(t, token, "team-a"), first) {
		t.Fatal("a team read could not clone the public mirror")
	}
	mirrored := func(commit string) bool {
		return exec.Command("git", "-C", mirrorPath(repoHash(repoURL), true), "cat-file", "-e", commit+"^{commit}").Run() == nil
	}

	pushed := commitTo(t, src, bare, "second.txt", "public")
	keepWarmPass(context.Background(), time.Minute)
	if !mirrored(pushed) {
		t.Error("the keep-warm pass did not refresh the public mirror a team read")
	}
	origin.setPrivate("public.git", true)
	hidden := commitTo(t, src, bare, "third.txt", "private now")
	keepWarmPass(context.Background(), time.Minute)
	if mirrored(hidden) {
		t.Error("the keep-warm pass fetched into the public mirror with the cache's credential")
	}
}
