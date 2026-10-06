package cache

import (
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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

// The operator seeds an unpublished commit into the mirror of an https origin.
// The operator still reads it; another team's grant sees neither its ref nor its
// objects, even when it asks for the commit by id.
func TestTeamGrantsNeverReadSeededCommits(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	root := t.TempDir()
	src := filepath.Join(root, "src")
	sha := commitTo(t, src, filepath.Join(root, "scratch.git"), "unpublished.txt", "private fixture data")
	ref := "refs/sparkwing-seed/" + sha
	runGit(t, src, "update-ref", ref, sha)
	bundle := filepath.Join(root, "seed.bundle")
	runGit(t, src, "bundle", "create", bundle, ref)
	raw, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}

	origin := "https://git.example.invalid/public/project.git"
	name := sourceurl.ClaimedRepoNameFromURL(origin)
	if code, body := send(t, srv, http.MethodPost, "/sync/seed?repo="+url.QueryEscape(origin)+"&sha="+sha, token, string(raw)); code != http.StatusOK {
		t.Fatalf("seed = %d: %s", code, body)
	}
	if code, body := send(t, srv, http.MethodPost, "/git/register?name="+name+"&repo="+url.QueryEscape(origin), token, ""); code != http.StatusOK {
		t.Fatalf("register = %d: %s", code, body)
	}
	t.Cleanup(func() {
		repoNamesMu.Lock()
		delete(repoNames, name)
		repoNamesMu.Unlock()
	})

	if code, body := send(t, srv, http.MethodGet, "/git/"+name+"/info/refs?service=git-upload-pack", token, ""); code != http.StatusOK || !strings.Contains(body, ref) {
		t.Errorf("operator reading its seeded mirror = %d, want 200 advertising %s", code, ref)
	}
	if !fetchFromCache(t, srv, name, token, sha) {
		t.Error("the operator could not fetch its own seeded commit")
	}
	grant := grantFor(t, token, "unrelated-team")
	if _, body := send(t, srv, http.MethodGet, "/git/"+name+"/info/refs?service=git-upload-pack", grant, ""); strings.Contains(body, sha) {
		t.Error("another team's grant was advertised the seeded commit")
	}
	if fetchFromCache(t, srv, name, grant, sha) {
		t.Error("another team's grant fetched the seeded commit by id")
	}
}

// A mirror the cache filled using a credential its own environment supplies
// holds a private repository behind an https URL. Another team's grant reads
// only what origin serves without a credential: the public repository, but not
// the private one, nor a commit pushed after the public one turned private.
func TestTeamGrantsReadOnlyWhatOriginServesAnonymously(t *testing.T) {
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
	// safety: the operator's refresh reaches the now-private origin with the cache's credential.
	send(t, srv, http.MethodPost, "/git/refresh?name="+publicName, token, "")
	if fetchFromCache(t, srv, publicName, grant, laterSHA) {
		t.Error("another team's grant fetched a commit pushed after its origin turned private")
	}
}
