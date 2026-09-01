package controller_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestGitcacheProxy_WorkspaceSeedForwardsBundleAndRetentionMarker(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/sync/seed" {
			t.Fatalf("path = %s, want /sync/seed", r.URL.Path)
		}
		if got := r.URL.Query().Get("repo"); got != "https://git.example.com/acme/widgets.git" {
			t.Fatalf("repo = %q", got)
		}
		if got := r.URL.Query().Get("sha"); got != sha {
			t.Fatalf("sha = %q", got)
		}
		if got := r.URL.Query().Get("workspace"); got != "1" {
			t.Fatalf("workspace = %q, want 1", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != "bundle" {
			t.Fatalf("body = %q, want bundle", body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer cache.Close()

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctrl := controller.New(st, nil).WithCacheURL(cache.URL)
	srv := httptest.NewServer(ctrl.Handler())
	defer srv.Close()

	resp, err := http.Post(
		srv.URL+"/api/v1/gitcache/seed?workspace=1&repo=https://git.example.com/acme/widgets.git&sha="+sha,
		"application/octet-stream",
		strings.NewReader("bundle"),
	)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
}

func TestGitcacheProxy_RejectsCacheRedirects(t *testing.T) {
	var targetRequests int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer cache.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	srv := httptest.NewServer(controller.New(st, nil).WithCacheURL(cache.URL).Handler())
	defer srv.Close()
	sha := strings.Repeat("a", 40)
	for name, request := range map[string]*http.Request{
		"seed": mustRequest(t, http.MethodPost,
			srv.URL+"/api/v1/gitcache/seed?workspace=1&repo=https://git.example.com/acme/widgets.git&sha="+sha,
			strings.NewReader("private bundle")),
		"git": mustRequest(t, http.MethodPost,
			srv.URL+"/api/v1/gitcache/git/widgets/git-upload-pack", strings.NewReader("want")),
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502", resp.StatusCode)
			}
		})
	}
	if targetRequests != 0 {
		t.Fatalf("redirect target requests = %d, want 0", targetRequests)
	}
}

func mustRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestGitcacheProxy_ReadsRequireAdminAndStripBearer(t *testing.T) {
	var cacheRequests []string
	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("cache received controller bearer %q", got)
		}
		cacheRequests = append(cacheRequests, r.Method+" "+r.URL.RequestURI())
		switch r.URL.Path {
		case "/git/register":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/git/widgets/info/refs":
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			_, _ = w.Write([]byte("refs"))
		case "/git/widgets/git-upload-pack":
			body, _ := io.ReadAll(r.Body)
			if string(body) != "want" {
				t.Fatalf("upload-pack body = %q", body)
			}
			_, _ = w.Write([]byte("pack"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cache.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	admin, _, err := st.CreateToken("admin", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	writer, _, err := st.CreateToken("writer", store.TokenKindUser, []string{controller.ScopeRunsWrite}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	ctrl := controller.New(st, nil).
		WithCacheURL(cache.URL).
		WithAuthenticator(controller.NewAuthenticator(st, time.Minute))
	srv := httptest.NewServer(ctrl.Handler())
	defer srv.Close()

	request := func(method, path, token, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := request(http.MethodPost, "/api/v1/gitcache/git/register?name=widgets&repo=https://git.example.com/acme/widgets.git", writer, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("runs.write register status = %d, want 403", resp.StatusCode)
	}
	resp = request(http.MethodPost, "/api/v1/gitcache/git/register?name=widgets&repo=https://git.example.com/acme/widgets.git", admin, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin register status = %d", resp.StatusCode)
	}
	resp = request(http.MethodGet, "/api/v1/gitcache/git/widgets/info/refs?service=git-upload-pack", admin, "")
	info, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(info) != "refs" {
		t.Fatalf("info refs status/body = %d/%q", resp.StatusCode, info)
	}
	resp = request(http.MethodPost, "/api/v1/gitcache/git/widgets/git-upload-pack", admin, "want")
	pack, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(pack) != "pack" {
		t.Fatalf("upload-pack status/body = %d/%q", resp.StatusCode, pack)
	}
	resp = request(http.MethodPost, "/api/v1/gitcache/git/widgets/git-receive-pack", admin, "push")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("receive-pack status = %d, want 405", resp.StatusCode)
	}
	if len(cacheRequests) != 3 {
		t.Fatalf("cache requests = %v", cacheRequests)
	}
}

func TestGitcacheProxy_AllowsSlowPackStreamBeyondDefaultDeadline(t *testing.T) {
	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "first")
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
		time.Sleep(60 * time.Millisecond)
		_, _ = io.WriteString(w, "second")
	}))
	defer cache.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	server := httptest.NewUnstartedServer(controller.New(st, nil).WithCacheURL(cache.URL).Handler())
	server.Config.WriteTimeout = 20 * time.Millisecond
	server.Start()
	defer server.Close()
	resp, err := http.Get(server.URL + "/api/v1/gitcache/git/widgets/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "firstsecond" {
		t.Fatalf("body = %q", body)
	}
}

func TestGitcacheProxy_AllowsSlowWorkspaceUploadBeyondDefaultDeadline(t *testing.T) {
	var gotBody string
	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotBody = string(body)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer cache.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	server := httptest.NewUnstartedServer(controller.New(st, nil).WithCacheURL(cache.URL).Handler())
	server.Config.ReadTimeout = 20 * time.Millisecond
	server.Start()
	defer server.Close()
	reader, writer := io.Pipe()
	go func() {
		_, _ = writer.Write([]byte("first"))
		time.Sleep(60 * time.Millisecond)
		_, _ = writer.Write([]byte("second"))
		_ = writer.Close()
	}()
	sha := strings.Repeat("a", 40)
	req, err := http.NewRequest(http.MethodPost,
		server.URL+"/api/v1/gitcache/seed?workspace=1&repo=https://git.example.com/acme/widgets.git&sha="+sha, reader)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || gotBody != "firstsecond" {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status/body/cache body = %d/%q/%q", resp.StatusCode, body, gotBody)
	}
}
