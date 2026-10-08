package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func TestHandleHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handleHealthCombined(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestResolveGitRepo_AutoClonesWhenMissing(t *testing.T) {
	resetFetchState(t)
	root := t.TempDir()

	upstream := filepath.Join(root, "upstream.git")
	if out, err := gitCmd("init", "--bare", upstream); err != nil {
		t.Fatalf("init upstream: %v (%s)", err, out)
	}

	oldRepoDir := repoDir
	oldNamesFile := namesFile
	repoDir = filepath.Join(root, "cache")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	namesFile = filepath.Join(root, "names.json")
	t.Cleanup(func() {
		repoDir = oldRepoDir
		namesFile = oldNamesFile
		repoNamesMu.Lock()
		delete(repoNames, "auto-clone-fixture")
		repoNamesMu.Unlock()
	})

	repoNamesMu.Lock()
	repoNames["auto-clone-fixture"] = upstream
	repoNamesMu.Unlock()

	bare, err := resolveGitRepo("auto-clone-fixture", false)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("cloned bare missing HEAD: %v", err)
	}

	bare2, err := resolveGitRepo("auto-clone-fixture", false)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if bare2 != bare {
		t.Fatalf("expected same bare path; got %q vs %q", bare2, bare)
	}
}

func TestResolveGitRepo_AutoCloneFailureNamesTheWayOut(t *testing.T) {
	resetFetchState(t)
	root := t.TempDir()
	oldRepoDir := repoDir
	repoDir = filepath.Join(root, "cache")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		repoDir = oldRepoDir
		repoNamesMu.Lock()
		delete(repoNames, "bad-url-fixture")
		repoNamesMu.Unlock()
	})

	repoNamesMu.Lock()
	repoNames["bad-url-fixture"] = "/this/path/does/not/exist.git"
	repoNamesMu.Unlock()

	_, err := resolveGitRepo("bad-url-fixture", false)
	if err == nil {
		t.Fatal("expected error from auto-clone of bogus URL")
	}
	if !strings.Contains(err.Error(), "re-register") || strings.Contains(err.Error(), "/sync/seed") {
		t.Fatalf("error should point operators at re-registering, not a removed route; got %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if args[0] == "init" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgSign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestRepoHash_Deterministic(t *testing.T) {
	h1 := repoHash("git@github.com:user/repo.git")
	h2 := repoHash("git@github.com:user/repo.git")
	if h1 != h2 {
		t.Error("same URL should produce same hash")
	}
	if len(h1) != 12 {
		t.Errorf("expected 12 char hash, got %d", len(h1))
	}
}

func TestRepoHash_Different(t *testing.T) {
	h1 := repoHash("git@github.com:user/repo1.git")
	h2 := repoHash("git@github.com:user/repo2.git")
	if h1 == h2 {
		t.Error("different URLs should produce different hashes")
	}
}

func TestHandleBinDigest(t *testing.T) {
	oldDir := binsDir
	binsDir = t.TempDir()
	defer func() { binsDir = oldDir }()

	const hash = "deadbeef-cafebabe"
	body := []byte("compiled pipeline bytes")
	sum := sha256.Sum256(body)
	wantDigest := "sha-256=" + base64.StdEncoding.EncodeToString(sum[:])

	put := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/bin/"+hash, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer writer-token")
	handleBin(put, req)
	if put.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d: %s", put.Code, put.Body.String())
	}
	if got := put.Header().Get("Digest"); got != wantDigest {
		t.Errorf("PUT Digest = %q, want %q", got, wantDigest)
	}

	meta, err := readBinMeta(binsDir, hash)
	if err != nil {
		t.Fatalf("readBinMeta: %v", err)
	}
	if meta.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("stored digest = %q, want %q", meta.SHA256, hex.EncodeToString(sum[:]))
	}
	if !strings.HasPrefix(meta.Principal, "token:") {
		t.Errorf("principal = %q, want a token fingerprint", meta.Principal)
	}
	if strings.Contains(meta.Principal, "writer-token") {
		t.Errorf("principal %q leaks the bearer", meta.Principal)
	}

	get := httptest.NewRecorder()
	handleBin(get, httptest.NewRequest(http.MethodGet, "/bin/"+hash, nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d", get.Code)
	}
	if got := get.Header().Get("Digest"); got != wantDigest {
		t.Errorf("GET Digest = %q, want %q", got, wantDigest)
	}
	if got := get.Header().Get("ETag"); got != `"`+hex.EncodeToString(sum[:])+`"` {
		t.Errorf("GET ETag = %q", got)
	}
	if !bytes.Equal(get.Body.Bytes(), body) {
		t.Errorf("GET body = %q, want %q", get.Body.Bytes(), body)
	}
}

func TestHandleBinDigestForUnattestedBlob(t *testing.T) {
	oldDir := binsDir
	binsDir = t.TempDir()
	defer func() { binsDir = oldDir }()

	const hash = "deadbeef-cafebabe"
	body := []byte("blob written before digests were recorded")
	if err := os.WriteFile(filepath.Join(binsDir, hash), body, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)

	w := httptest.NewRecorder()
	handleBin(w, httptest.NewRequest(http.MethodGet, "/bin/"+hash, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d", w.Code)
	}
	if got := w.Header().Get("Digest"); got != "sha-256="+base64.StdEncoding.EncodeToString(sum[:]) {
		t.Errorf("GET Digest = %q", got)
	}
	if !bytes.Equal(w.Body.Bytes(), body) {
		t.Errorf("GET body = %q, want %q", w.Body.Bytes(), body)
	}
	meta, err := readBinMeta(binsDir, hash)
	if err != nil {
		t.Fatalf("readBinMeta: %v", err)
	}
	if meta.Principal != "unknown" {
		t.Errorf("principal = %q, want unknown", meta.Principal)
	}
}

func TestRequireTokenBlocksAnUnauthenticatedBinPut(t *testing.T) {
	oldDir, oldToken := binsDir, apiToken
	binsDir, apiToken = t.TempDir(), "cache-token"
	defer func() { binsDir, apiToken = oldDir, oldToken }()

	const hash = "deadbeef-cafebabe"
	w := httptest.NewRecorder()
	requireToken(handleBin)(w, httptest.NewRequest(http.MethodPut, "/bin/"+hash, strings.NewReader("poisoned")))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if _, err := os.Stat(filepath.Join(binsDir, hash)); !os.IsNotExist(err) {
		t.Fatalf("unauthorized PUT stored a blob: %v", err)
	}
}

func TestHandleBinClientRejectsPoisonedBlob(t *testing.T) {
	oldDir := binsDir
	binsDir = t.TempDir()
	defer func() { binsDir = oldDir }()

	const hash = "deadbeef-cafebabe"
	srv := httptest.NewServer(http.HandlerFunc(handleBin))
	defer srv.Close()

	src := filepath.Join(t.TempDir(), "pipeline")
	if err := os.WriteFile(src, []byte("compiled pipeline bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := bincache.UploadBinary(context.Background(), srv.URL, "writer-token", hash, src); err != nil {
		t.Fatalf("UploadBinary: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "pipeline")
	if err := bincache.TryBinary(context.Background(), srv.URL, "writer-token", hash, dest); err != nil {
		t.Fatalf("TryBinary: %v", err)
	}

	if err := os.WriteFile(filepath.Join(binsDir, hash), []byte("poisoned bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	poisoned := filepath.Join(t.TempDir(), "pipeline")
	if err := bincache.TryBinary(context.Background(), srv.URL, "writer-token", hash, poisoned); !errors.Is(err, bincache.ErrDigest) {
		t.Fatalf("err = %v, want ErrDigest", err)
	}
	if _, err := os.Stat(poisoned); !os.IsNotExist(err) {
		t.Fatalf("poisoned binary was installed: %v", err)
	}
}

func TestRequireToken(t *testing.T) {
	old := apiToken
	apiToken = "s3cret"
	defer func() { apiToken = old }()

	for _, tc := range []struct {
		name      string
		authz     string
		forwarded string
		want      int
	}{
		{name: "correct bearer", authz: "Bearer s3cret", want: http.StatusOK},
		{name: "wrong bearer", authz: "Bearer nope", want: http.StatusUnauthorized},
		{name: "no header", want: http.StatusUnauthorized},
		{name: "no header, forwarded", forwarded: "203.0.113.7", want: http.StatusUnauthorized},
		{name: "wrong bearer, forwarded", authz: "Bearer nope", forwarded: "203.0.113.7", want: http.StatusUnauthorized},
		{name: "lowercase scheme", authz: "bearer s3cret", want: http.StatusOK},
		{name: "padded scheme", authz: "Bearer  s3cret", want: http.StatusOK},
		{name: "no scheme", authz: "s3cret", want: http.StatusUnauthorized},
		{name: "other scheme", authz: "Basic s3cret", want: http.StatusUnauthorized},
		{name: "empty credential", authz: "Bearer ", want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			served := false
			h := requireToken(func(w http.ResponseWriter, _ *http.Request) {
				served = true
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodPut, "/bin/abc", nil)
			if tc.authz != "" {
				req.Header.Set("Authorization", tc.authz)
			}
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			w := httptest.NewRecorder()
			h(w, req)

			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
			if served != (tc.want == http.StatusOK) {
				t.Errorf("handler served = %t, want %t", served, tc.want == http.StatusOK)
			}
		})
	}
}

func TestRequireTokenServesEveryoneWhenUnauthenticated(t *testing.T) {
	old := apiToken
	apiToken = ""
	defer func() { apiToken = old }()

	served := false
	h := requireToken(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	})
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPut, "/bin/abc", nil))

	if !served || w.Code != http.StatusOK {
		t.Errorf("served = %t, status = %d, want true and 200", served, w.Code)
	}
}

func TestNewRejectsEmptyAPIToken(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()

	if _, err := New(cfg); err == nil {
		t.Fatal("New accepted an empty API token")
	} else if !strings.Contains(err.Error(), "--allow-unauthenticated") {
		t.Errorf("error %q does not name the opt-in flag", err)
	}
}

func newTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	cfg := DefaultConfig()
	cfg.APIToken = token
	cfg.AllowUnauthenticated = token == ""
	return newTestServerConfig(t, cfg)
}

// safety: New copies this config into the package globals, so a test that needs
// a different window sets it here rather than writing those globals while the
// server is answering requests.
func newTestServerConfig(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	saved := struct {
		dataRoot, repoDir, binsDir, cacheDir     string
		namesFile, proxyDir, sshKeyDir, apiToken string
	}{
		dataRoot, repoDir, binsDir, cacheDir,
		namesFile, proxyDir, sshKeyDir, apiToken,
	}
	t.Cleanup(func() {
		dataRoot, repoDir, binsDir, cacheDir = saved.dataRoot, saved.repoDir, saved.binsDir, saved.cacheDir
		namesFile, proxyDir, sshKeyDir, apiToken = saved.namesFile, saved.proxyDir, saved.sshKeyDir, saved.apiToken
	})

	root := t.TempDir()
	cfg.DataDir = root
	cfg.ProxyDir = filepath.Join(root, "proxy")
	cfg.SSHKeyDir = filepath.Join(root, "no-ssh-key")
	// safety: New replaces globals that a store measurement still running from an earlier test reads.
	measureOnce.Wait()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestMuxGuardsEveryWriteRoute(t *testing.T) {
	srv := newTestServer(t, "s3cret")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"left-pad"}`))
	}))
	defer upstream.Close()

	cases := []struct {
		method, path string
		guarded      bool
	}{
		{method: http.MethodGet, path: "/bin/deadbeef-cafebabe", guarded: true},
		{method: http.MethodPut, path: "/bin/deadbeef-cafebabe", guarded: true},
		{method: http.MethodPut, path: "/cache/lint", guarded: true},
		{method: http.MethodGet, path: "/cache/lint", guarded: true},
		{method: http.MethodPost, path: "/admin/store-ceiling/thaw", guarded: true},
		{method: http.MethodPost, path: "/admin/store-ceiling/measure", guarded: true},
		{method: http.MethodDelete, path: "/admin/teams/acme", guarded: true},
		{method: http.MethodPost, path: "/git/app/git-upload-pack", guarded: true},
		{method: http.MethodPost, path: "/git/register?name=app&repo=https://example.com/a.git", guarded: true},
		{method: http.MethodGet, path: "/git/app/info/refs?service=git-upload-pack", guarded: true},
		{method: http.MethodGet, path: "/health", guarded: false},
		{method: http.MethodGet, path: "/stats", guarded: false},
		{method: http.MethodGet, path: "/metrics", guarded: false},
		{method: http.MethodGet, path: "/proxy/npm/left-pad", guarded: false},
	}

	withTestProxy(t, map[string]Registry{
		"npm": {Name: "npm", Upstream: upstream.URL},
	}, func() {
		for _, tc := range cases {
			t.Run(tc.method+" "+tc.path, func(t *testing.T) {
				req, err := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader(""))
				if err != nil {
					t.Fatal(err)
				}
				resp, err := srv.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if got := resp.StatusCode == http.StatusUnauthorized; got != tc.guarded {
					t.Errorf("status = %d, guarded = %t, want guarded = %t", resp.StatusCode, got, tc.guarded)
				}
			})
		}
	})
}

func TestNewRejectsAWhitespaceOnlyAPIToken(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.APIToken = " \n"

	if _, err := New(cfg); err == nil {
		t.Fatal("New accepted a whitespace-only API token")
	} else if !strings.Contains(err.Error(), "--allow-unauthenticated") {
		t.Errorf("error %q does not name the opt-in flag", err)
	}
}

func TestHandleBinFailedPutLeavesNoSidecar(t *testing.T) {
	oldDir := binsDir
	binsDir = t.TempDir()
	defer func() { binsDir = oldDir }()

	const hash = "deadbeef-cafebabe"
	if err := os.MkdirAll(filepath.Join(binsDir, hash, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handleBin(w, httptest.NewRequest(http.MethodPut, "/bin/"+hash, strings.NewReader("compiled pipeline bytes")))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("PUT status = %d, want 500", w.Code)
	}
	if _, err := os.Stat(binMetaPath(binsDir, hash)); !os.IsNotExist(err) {
		t.Fatalf("failed PUT left a sidecar attesting bytes that were never stored: %v", err)
	}
	entries, err := os.ReadDir(binsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("failed PUT left a staged blob %s", e.Name())
		}
	}
}

func TestHandleBinLegacyGetRacingAPutKeepsTheSidecarHonest(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.7s of real work; the fast class runs under -short")
	}
	for i := 0; i < 25; i++ {
		oldDir := binsDir
		binsDir = t.TempDir()

		const hash = "deadbeef-cafebabe"
		legacy := bytes.Repeat([]byte("legacy blob "), 1<<18)
		if err := os.WriteFile(filepath.Join(binsDir, hash), legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		fresh := bytes.Repeat([]byte("fresh blob "), 1<<18)

		var wg sync.WaitGroup
		get := httptest.NewRecorder()
		wg.Add(2)
		go func() {
			defer wg.Done()
			handleBin(get, httptest.NewRequest(http.MethodGet, "/bin/"+hash, nil))
		}()
		go func() {
			defer wg.Done()
			put := httptest.NewRecorder()
			handleBin(put, httptest.NewRequest(http.MethodPut, "/bin/"+hash, bytes.NewReader(fresh)))
		}()
		wg.Wait()

		blob, err := os.ReadFile(filepath.Join(binsDir, hash))
		if err != nil {
			t.Fatal(err)
		}
		onDisk := sha256.Sum256(blob)
		meta, err := readBinMeta(binsDir, hash)
		if err != nil {
			t.Fatalf("readBinMeta: %v", err)
		}
		if meta.SHA256 != hex.EncodeToString(onDisk[:]) {
			t.Fatalf("iteration %d: sidecar %s attests neither blob on disk (%s)", i, meta.SHA256, hex.EncodeToString(onDisk[:]))
		}
		if get.Code == http.StatusOK {
			served := sha256.Sum256(get.Body.Bytes())
			want := "sha-256=" + base64.StdEncoding.EncodeToString(served[:])
			if got := get.Header().Get("Digest"); got != want {
				t.Fatalf("iteration %d: served Digest = %q, body hashes to %q", i, got, want)
			}
		}
		binsDir = oldDir
	}
}

func TestEveryResponseCarriesNosniff(t *testing.T) {
	srv := newTestServer(t, "s3cret")

	for _, path := range []string{"/health", "/bin/deadbeef-cafebabe", "/metrics"} {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s X-Content-Type-Options = %q, want nosniff", path, got)
		}
	}
}

func TestGitRegisterValidatesTheName(t *testing.T) {
	srv := newTestServer(t, "s3cret")

	for _, name := range []string{"../escape", "a/b", strings.Repeat("n", 65), "na me", ""} {
		req, err := http.NewRequest(http.MethodPost,
			srv.URL+"/git/register?repo=https%3A%2F%2Fexample.invalid%2Fa.git&name="+url.QueryEscape(name), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer s3cret")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("register name %q = %d, want 400", name, resp.StatusCode)
		}
	}
}

func TestGitRegisterRepairsSeededMirrorOrigin(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing-origin=%v", existing), func(t *testing.T) {
			srv := newTestServer(t, "s3cret")
			isolateRepoNames(t)
			repoURL := "https://example.invalid/acme/widgets.git"
			bareRepo := filepath.Join(repoDir, repoHash(repoURL)+".git")
			runGit(t, bareRepo, "init", "--bare")
			if existing {
				runGit(t, bareRepo, "remote", "add", "origin", repoURL)
			}
			if code := registerName(t, srv, sourceurl.ClaimedRepoNameFromURL(repoURL), repoURL, "s3cret"); code != http.StatusOK {
				t.Fatalf("registration = %d, want 200", code)
			}
			if got := strings.TrimSpace(runGit(t, bareRepo, "config", "--get", "remote.origin.url")); got != repoURL {
				t.Fatalf("registered origin = %q, want %q", got, repoURL)
			}
		})
	}
}

func TestGitRegisterReportsOriginConfigFailure(t *testing.T) {
	srv := newTestServer(t, "s3cret")
	isolateRepoNames(t)
	repoURL := "https://example.invalid/acme/widgets.git"
	bareRepo := filepath.Join(repoDir, repoHash(repoURL)+".git")
	runGit(t, bareRepo, "init", "--bare")
	if err := os.WriteFile(filepath.Join(bareRepo, "config.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := registerName(t, srv, sourceurl.ClaimedRepoNameFromURL(repoURL), repoURL, "s3cret"); code != http.StatusInternalServerError {
		t.Fatalf("registration with locked config = %d, want 500", code)
	}
}

func TestGitRegisterAcceptsClaimScopedRepoName(t *testing.T) {
	srv := newTestServer(t, "s3cret")
	repoURL := "https://example.invalid/acme/widgets.git"
	name := sourceurl.ClaimedRepoNameFromURL(repoURL)
	if code := registerName(t, srv, name, repoURL, "s3cret"); code != http.StatusOK {
		t.Fatalf("register claim-scoped name length %d = %d, want 200", len(name), code)
	}
}

func isolateRepoNames(t *testing.T) {
	t.Helper()
	repoNamesMu.Lock()
	saved := repoNames
	repoNames = map[string]string{}
	repoNamesMu.Unlock()
	t.Cleanup(func() {
		repoNamesMu.Lock()
		repoNames = saved
		repoNamesMu.Unlock()
	})
}

func registerName(t *testing.T, srv *httptest.Server, name, repo, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/git/register?name="+url.QueryEscape(name)+"&repo="+url.QueryEscape(repo), nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func registeredRepo(name string) string {
	repoNamesMu.RLock()
	defer repoNamesMu.RUnlock()
	return repoNames[name]
}

func TestGitRegisterRefusesAnUnauthenticatedRepoint(t *testing.T) {
	srv := newTestServer(t, "s3cret")
	isolateRepoNames(t)

	if code := registerName(t, srv, "app", "https://example.invalid/first.git", "s3cret"); code != http.StatusOK {
		t.Fatalf("first registration = %d, want 200", code)
	}
	if code := registerName(t, srv, "app", "https://example.invalid/second.git", ""); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated repoint = %d, want 401", code)
	}
	if got := registeredRepo("app"); got != "https://example.invalid/first.git" {
		t.Errorf("registered repo = %q, want the original", got)
	}
	if code := registerName(t, srv, "app", "https://example.invalid/second.git", "s3cret"); code != http.StatusOK {
		t.Errorf("authenticated repoint = %d, want 200", code)
	}
	if got := registeredRepo("app"); got != "https://example.invalid/second.git" {
		t.Errorf("registered repo = %q, want the repointed one", got)
	}
}

func TestGitRegisterAllowsARepointOnAnOpenCache(t *testing.T) {
	srv := newTestServer(t, "")
	isolateRepoNames(t)

	if code := registerName(t, srv, "app", "https://example.invalid/first.git", ""); code != http.StatusOK {
		t.Fatalf("first registration = %d, want 200", code)
	}
	if code := registerName(t, srv, "app", "https://example.invalid/second.git", ""); code != http.StatusOK {
		t.Errorf("repoint on an open cache = %d, want 200", code)
	}
	if got := registeredRepo("app"); got != "https://example.invalid/second.git" {
		t.Errorf("registered repo = %q, want the repointed one", got)
	}
}

func TestAutoRegisterSkipsAnInvalidName(t *testing.T) {
	newTestServer(t, "s3cret")
	isolateRepoNames(t)

	saved := autoRegisterReposSpec
	autoRegisterReposSpec = "a/../b=https://example.invalid/a.git,ok=https://example.invalid/b.git"
	t.Cleanup(func() { autoRegisterReposSpec = saved })

	autoRegisterRepos()

	if got := registeredRepo("a/../b"); got != "" {
		t.Errorf("auto-register accepted an invalid name: %q", got)
	}
	if got := registeredRepo("ok"); got != "https://example.invalid/b.git" {
		t.Errorf("auto-register dropped a valid entry: %q", got)
	}
}

func TestMetricsDoNotEnumerateMirrors(t *testing.T) {
	srv := newTestServer(t, "s3cret")
	isolateRepoNames(t)

	const repoURL = "https://git.example.invalid/acme/secret-service.git"
	hash := repoHash(repoURL)
	repoNamesMu.Lock()
	repoNames["secret-service"] = repoURL
	repoNamesMu.Unlock()
	runGit(t, filepath.Join(repoDir, hash+".git"), "init", "--bare")
	bgFetch.markRequested(stateKey(hash))

	startBackgroundFetch(t, time.Millisecond)

	deadline := time.Now().Add(10 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		resp, err := srv.Client().Get(srv.URL + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		body = string(raw)
		if strings.Contains(body, "sparkwing_gitcache_fetch_duration_seconds") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(body, "sparkwing_gitcache_fetch_duration_seconds") {
		t.Fatalf("/metrics never exported the fetch histogram:\n%s", body)
	}
	for _, leak := range []string{hash, "secret-service", `repo="`} {
		if strings.Contains(body, leak) {
			t.Errorf("/metrics leaks %q:\n%s", leak, body)
		}
	}
}

func TestSetupSSHFailsWhenTheKeyCannotBeStaged(t *testing.T) {
	saved := sshKeyDir
	t.Cleanup(func() { sshKeyDir = saved })
	root := t.TempDir()

	sshKeyDir = filepath.Join(root, "absent")
	if err := setupSSH(); err != nil {
		t.Fatalf("setupSSH with no key secret: %v", err)
	}

	sshKeyDir = filepath.Join(root, "key")
	if err := os.MkdirAll(sshKeyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshKeyDir, "id_ed25519"), []byte("private-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.WriteFile(home, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	err := setupSSH()
	if err == nil {
		t.Fatal("setupSSH accepted a home it cannot write, leaving every private-repo mirror keyless")
	}
	if !strings.Contains(err.Error(), "stage SSH key") {
		t.Fatalf("err = %v, want it to name the staging step", err)
	}
}

func TestSetupSSHKeepsTheOperatorsGitSSHCommand(t *testing.T) {
	saved := sshKeyDir
	t.Cleanup(func() { sshKeyDir = saved })
	root := t.TempDir()
	sshKeyDir = filepath.Join(root, "key")
	if err := os.MkdirAll(sshKeyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshKeyDir, "id_ed25519"), []byte("private-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", "")

	const operator = "ssh -i /etc/ssh-key/id_ed25519 -o IdentitiesOnly=yes"
	t.Setenv("GIT_SSH_COMMAND", operator)
	if err := setupSSH(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("GIT_SSH_COMMAND"); got != operator {
		t.Fatalf("GIT_SSH_COMMAND = %q, want the operator's %q", got, operator)
	}

	t.Setenv("GIT_SSH_COMMAND", "")
	if err := setupSSH(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("GIT_SSH_COMMAND"); !strings.Contains(got, "-o IdentitiesOnly=yes") {
		t.Fatalf("GIT_SSH_COMMAND = %q, want it to offer only the staged key", got)
	}
}

func TestLoadRepoNamesDropsEntriesTheCloneValidatorRefuses(t *testing.T) {
	root := t.TempDir()
	oldNamesFile := namesFile
	namesFile = filepath.Join(root, "names.json")
	t.Cleanup(func() {
		namesFile = oldNamesFile
		repoNamesMu.Lock()
		delete(repoNames, "good-entry")
		delete(repoNames, "hostile-entry")
		repoNamesMu.Unlock()
	})

	stored := map[string]string{
		"good-entry":    "https://git.example.com/acme/widgets.git",
		"hostile-entry": "--upload-pack=/tmp/pwned",
	}
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(namesFile, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	loadRepoNames()

	repoNamesMu.RLock()
	good, okGood := repoNames["good-entry"]
	_, okHostile := repoNames["hostile-entry"]
	repoNamesMu.RUnlock()

	if !okGood || good != stored["good-entry"] {
		t.Errorf("repoNames[good-entry] = %q, %v, want the stored URL", good, okGood)
	}
	if okHostile {
		t.Error("repoNames kept an entry whose URL git would read as an option")
	}
	if !strings.Contains(logged.String(), `dropping repo "hostile-entry"`) {
		t.Errorf("log = %q, want it to name the dropped entry", logged.String())
	}
}
