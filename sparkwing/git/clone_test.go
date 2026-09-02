package git

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestResolveCloneURL_NoCache(t *testing.T) {
	t.Setenv("SPARKWING_GITCACHE", "")
	t.Setenv("SPARKWING_GITCACHE_URL", "")

	prev := gitcacheProbeURL
	gitcacheProbeURL = "http://127.0.0.1:1"
	defer func() { gitcacheProbeURL = prev }()

	upstream := "git@github.com:owner/repo.git"
	got, _, _ := resolveCloneURL(context.Background(), upstream)
	if got != upstream {
		t.Fatalf("got %q, want upstream %q", got, upstream)
	}
}

func TestResolveCloneURL_StubServerNotHealthy(t *testing.T) {
	t.Setenv("SPARKWING_GITCACHE", "")
	t.Setenv("SPARKWING_GITCACHE_URL", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	prev := gitcacheProbeURL
	gitcacheProbeURL = srv.URL
	defer func() { gitcacheProbeURL = prev }()

	upstream := "git@github.com:owner/repo.git"
	got, _, _ := resolveCloneURL(context.Background(), upstream)
	if got != upstream {
		t.Fatalf("404 health: got %q, want upstream %q", got, upstream)
	}
}

func TestResolveCloneURL_StubServerHealthy(t *testing.T) {
	t.Setenv("SPARKWING_GITCACHE", "")
	t.Setenv("SPARKWING_GITCACHE_URL", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	prev := gitcacheProbeURL
	gitcacheProbeURL = srv.URL
	defer func() { gitcacheProbeURL = prev }()

	upstream := "git@github.com:owner/repo.git"
	got, _, _ := resolveCloneURL(context.Background(), upstream)
	want := srv.URL + "/git/repo"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveCloneURL_EnvOverride(t *testing.T) {
	t.Setenv("SPARKWING_GITCACHE", "http://cache.local:9999/")
	t.Setenv("SPARKWING_GITCACHE_URL", "")

	upstream := "https://github.com/owner/repo.git"
	got, _, _ := resolveCloneURL(context.Background(), upstream)
	want := "http://cache.local:9999/git/repo"
	if got != want {
		t.Fatalf("env override: got %q, want %q", got, want)
	}
}

func TestCacheCloneURL_FormatVariants(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"ssh", "git@github.com:owner/repo.git", "http://c/git/repo"},
		{"https", "https://github.com/owner/repo.git", "http://c/git/repo"},
		{"no-suffix", "git@host:owner/repo", "http://c/git/repo"},
		{"trailing-slash-base", "git@github.com:owner/repo.git", "http://c/git/repo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cacheCloneURL("http://c", c.in)
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			if !strings.HasPrefix(got, "http://c/") {
				t.Fatalf("base lost: %q", got)
			}
		})
	}
}

func TestCloneThroughSecuredGitcache(t *testing.T) {
	cases := []struct {
		name       string
		token      string
		wantFile   string
		wantBearer string
	}{
		{name: "token accepted", token: "s3cret", wantFile: "cached.txt", wantBearer: "Bearer s3cret"},
		{name: "no token", token: "", wantFile: "upstream.txt"},
		{name: "wrong token", token: "nope", wantFile: "upstream.txt", wantBearer: "Bearer nope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			upstream := newTestRepo(t, filepath.Join(root, "upstream"), "upstream.txt")
			cached := bareTestRepo(t, root, newTestRepo(t, filepath.Join(root, "cached"), "cached.txt"))
			srv, seen := tokenedGitcache(t, "s3cret", cached)

			t.Setenv("SPARKWING_GITCACHE", srv.URL)
			t.Setenv("SPARKWING_GITCACHE_URL", "")
			t.Setenv("SPARKWING_CACHE_TOKEN", c.token)

			dest := filepath.Join(root, "dest")
			if err := Clone(context.Background(), upstream, dest); err != nil {
				t.Fatalf("Clone: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dest, c.wantFile)); err != nil {
				t.Fatalf("clone did not come from the expected source: %v", err)
			}
			if got := seen(); got != c.wantBearer {
				t.Fatalf("cache saw Authorization %q, want %q", got, c.wantBearer)
			}
		})
	}
}

func TestGitcacheEnvKeepsBearerOffTheCommandLine(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "")

	env := gitcacheEnv("http://cache.local:9999/", "s3cret")
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=http.http://cache.local:9999/.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Bearer s3cret",
		"GIT_CONFIG_KEY_1=http.http://cache.local:9999/.followRedirects",
		"GIT_CONFIG_VALUE_1=false",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("gitcacheEnv is missing %q", want)
		}
	}

	untokened := strings.Join(gitcacheEnv("http://cache.local:9999", ""), "\n")
	if strings.Contains(untokened, "extraHeader") {
		t.Error("gitcacheEnv sends an extraHeader with no token")
	}
	if !strings.Contains(untokened, "GIT_CONFIG_COUNT=1") {
		t.Error("gitcacheEnv miscounts its config entries without a token")
	}
}

func TestGitcacheEnvPreservesInheritedConfigEntries(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "someone")

	joined := strings.Join(gitcacheEnv("http://cache.local:9999", "s3cret"), "\n")
	if !strings.Contains(joined, "GIT_CONFIG_COUNT=3") {
		t.Error("inherited GIT_CONFIG_COUNT was not extended")
	}
	if !strings.Contains(joined, "GIT_CONFIG_KEY_1=http.http://cache.local:9999/.extraHeader") {
		t.Error("the bearer entry overwrote an inherited config slot")
	}
}

func TestRejectedByCache(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"prompts disabled", "fatal: could not read Username for 'http://c': terminal prompts disabled", true},
		{"explicit 401", "fatal: unable to access 'http://c/': The requested URL returned error: 401", true},
		{"auth failed", "fatal: Authentication failed for 'http://c/'", true},
		{"redirecting ingress", "fatal: unable to access 'http://c/': The requested URL returned error: 301", true},
		{"temporary redirect", "fatal: unable to access 'http://c/': The requested URL returned error: 307", true},
		{"server error", "fatal: unable to access 'http://c/': The requested URL returned error: 500", false},
		{"missing repo", "fatal: repository 'http://c/git/repo' not found", false},
		{"connection refused", "fatal: unable to access 'http://c/': Failed to connect", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rejectedByCache(errors.New(c.msg)); got != c.want {
				t.Fatalf("rejectedByCache(%q) = %t, want %t", c.msg, got, c.want)
			}
		})
	}
}

func newTestRepo(t *testing.T, dir, file string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "add", file)
	runTestGit(t, dir, "commit", "--quiet", "-m", "seed")
	return dir
}

func bareTestRepo(t *testing.T, root, src string) string {
	t.Helper()
	bare := filepath.Join(root, "bare.git")
	runTestGit(t, root, "clone", "--quiet", "--bare", src, bare)
	runTestGit(t, bare, "update-server-info")
	return bare
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=sparkwing", "GIT_AUTHOR_EMAIL=sparkwing@example.invalid",
		"GIT_COMMITTER_NAME=sparkwing", "GIT_COMMITTER_EMAIL=sparkwing@example.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func tokenedGitcache(t *testing.T, token, bare string) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var authorization string
	files := http.FileServer(http.Dir(bare))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		mu.Lock()
		authorization = got
		mu.Unlock()
		if got != "Bearer "+token {
			http.Error(w, "unauthorized -- set Authorization: Bearer <token> header", http.StatusUnauthorized)
			return
		}
		rest := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/git/"), "/", 2)
		if len(rest) != 2 {
			http.NotFound(w, r)
			return
		}
		served := r.Clone(r.Context())
		served.URL.Path = "/" + rest[1]
		files.ServeHTTP(w, served)
	}))
	t.Cleanup(srv.Close)
	return srv, func() string {
		mu.Lock()
		defer mu.Unlock()
		return authorization
	}
}

func TestProbedCacheNeverSeesTheBearer(t *testing.T) {
	root := t.TempDir()
	upstream := newTestRepo(t, filepath.Join(root, "upstream"), "upstream.txt")
	cached := bareTestRepo(t, root, newTestRepo(t, filepath.Join(root, "cached"), "cached.txt"))
	srv, seen := probingGitcache(t, "s3cret", cached)

	t.Setenv("SPARKWING_GITCACHE", "")
	t.Setenv("SPARKWING_GITCACHE_URL", "")
	t.Setenv("SPARKWING_CACHE_TOKEN", "s3cret")

	prev := gitcacheProbeURL
	gitcacheProbeURL = srv.URL
	defer func() { gitcacheProbeURL = prev }()

	dest := filepath.Join(root, "dest")
	if err := Clone(context.Background(), upstream, dest); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if got := seen(); got != "" {
		t.Fatalf("the probed cache saw Authorization %q, want none", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "upstream.txt")); err != nil {
		t.Fatalf("clone did not fall back to upstream: %v", err)
	}
}

func TestGitcacheURLEnvRoutesClonesThroughTheCache(t *testing.T) {
	root := t.TempDir()
	upstream := newTestRepo(t, filepath.Join(root, "upstream"), "upstream.txt")
	cached := bareTestRepo(t, root, newTestRepo(t, filepath.Join(root, "cached"), "cached.txt"))
	srv, seen := tokenedGitcache(t, "s3cret", cached)

	t.Setenv("SPARKWING_GITCACHE", "")
	t.Setenv("SPARKWING_GITCACHE_URL", srv.URL)
	t.Setenv("SPARKWING_CACHE_TOKEN", "s3cret")

	dest := filepath.Join(root, "dest")
	if err := Clone(context.Background(), upstream, dest); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "cached.txt")); err != nil {
		t.Fatalf("SPARKWING_GITCACHE_URL did not route the clone through the cache: %v", err)
	}
	if got := seen(); got != "Bearer s3cret" {
		t.Fatalf("cache saw Authorization %q, want the bearer", got)
	}
}

func TestCloneFallsBackWhenTheCacheRedirects(t *testing.T) {
	root := t.TempDir()
	upstream := newTestRepo(t, filepath.Join(root, "upstream"), "upstream.txt")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.invalid"+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer srv.Close()

	t.Setenv("SPARKWING_GITCACHE", srv.URL)
	t.Setenv("SPARKWING_GITCACHE_URL", "")
	t.Setenv("SPARKWING_CACHE_TOKEN", "s3cret")

	dest := filepath.Join(root, "dest")
	if err := Clone(context.Background(), upstream, dest); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "upstream.txt")); err != nil {
		t.Fatalf("a redirecting cache did not fall back to upstream: %v", err)
	}
}

func TestFetchAfterCachedCloneCarriesTheBearer(t *testing.T) {
	root := t.TempDir()
	upstream := newTestRepo(t, filepath.Join(root, "upstream"), "upstream.txt")
	cached := bareTestRepo(t, root, newTestRepo(t, filepath.Join(root, "cached"), "cached.txt"))
	srv, seen := tokenedGitcache(t, "s3cret", cached)

	t.Setenv("SPARKWING_GITCACHE", srv.URL)
	t.Setenv("SPARKWING_GITCACHE_URL", "")
	t.Setenv("SPARKWING_CACHE_TOKEN", "s3cret")

	dest := filepath.Join(root, "dest")
	if err := Clone(context.Background(), upstream, dest); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if err := Fetch(context.Background(), dest); err != nil {
		t.Fatalf("Fetch after a cached clone: %v", err)
	}
	if got := seen(); got != "Bearer s3cret" {
		t.Fatalf("cache saw Authorization %q on fetch, want the bearer", got)
	}
}

func TestFetchLeavesANonCacheOriginAlone(t *testing.T) {
	root := t.TempDir()
	upstream := newTestRepo(t, filepath.Join(root, "upstream"), "upstream.txt")

	t.Setenv("SPARKWING_GITCACHE", "http://cache.local:9999")
	t.Setenv("SPARKWING_GITCACHE_URL", "")
	t.Setenv("SPARKWING_CACHE_TOKEN", "s3cret")

	dest := filepath.Join(root, "dest")
	runTestGit(t, root, "clone", "--quiet", upstream, dest)
	if err := Fetch(context.Background(), dest); err != nil {
		t.Fatalf("Fetch from a local origin: %v", err)
	}
}

func TestGitcacheEnvIgnoresAMalformedInheritedCount(t *testing.T) {
	for _, count := range []string{"-1", "abc", "", " ", "99999999999999999999"} {
		t.Run("count="+count, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_COUNT", count)

			joined := strings.Join(gitcacheEnv("http://cache.local:9999", "s3cret"), "\n")
			for _, want := range []string{
				"GIT_CONFIG_COUNT=2",
				"GIT_CONFIG_KEY_0=http.http://cache.local:9999/.extraHeader",
				"GIT_CONFIG_VALUE_0=Authorization: Bearer s3cret",
				"GIT_CONFIG_KEY_1=http.http://cache.local:9999/.followRedirects",
			} {
				if !strings.Contains(joined, want) {
					t.Errorf("gitcacheEnv is missing %q", want)
				}
			}
		})
	}
}

func probingGitcache(t *testing.T, token, bare string) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var authorization string
	files := http.FileServer(http.Dir(bare))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		got := r.Header.Get("Authorization")
		mu.Lock()
		if got != "" {
			authorization = got
		}
		mu.Unlock()
		if got != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		rest := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/git/"), "/", 2)
		if len(rest) != 2 {
			http.NotFound(w, r)
			return
		}
		served := r.Clone(r.Context())
		served.URL.Path = "/" + rest[1]
		files.ServeHTTP(w, served)
	}))
	t.Cleanup(srv.Close)
	return srv, func() string {
		mu.Lock()
		defer mu.Unlock()
		return authorization
	}
}
