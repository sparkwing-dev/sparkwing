package controller_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

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
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	srv := httptest.NewServer(controller.New(st, nil).WithCacheCredentials(cache.URL, "cache-secret").Handler())
	defer srv.Close()
	for name, request := range map[string]*http.Request{
		"register": mustRequest(t, http.MethodPost,
			srv.URL+"/api/v1/gitcache/git/register?name=widgets&repo=https://git.example.com/acme/widgets.git", nil),
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
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	var cacheRequests []string
	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cache-secret" {
			t.Fatalf("cache Authorization = %q, want the cache token and never the caller's", got)
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

	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
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
		WithCacheCredentials(cache.URL, "cache-secret").
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

func TestGitcacheProxy_ClaimedRunnerReadsOnlyItsRunSource(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	repoURL := "git@github.com:acme/widgets.git"
	cacheName := sourceurl.ClaimedRepoNameFromURL(repoURL)
	var cacheRequests []string
	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cache-secret" {
			t.Fatalf("cache Authorization = %q, want cache token", got)
		}
		cacheRequests = append(cacheRequests, r.Method+" "+r.URL.RequestURI())
		switch r.URL.Path {
		case "/git/register":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/git/" + cacheName + "/info/refs":
			_, _ = w.Write([]byte("refs"))
		case "/git/" + cacheName + "/git-upload-pack":
			_, _ = w.Write([]byte("pack"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cache.Close()

	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	now := time.Now().UTC()
	owner, _, err := st.CreateToken("workstation", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	stranger, _, err := st.CreateToken("stranger", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "run-remote", Pipeline: "build", Status: "running", CreatedAt: now,
		Repo: "acme/widgets", RepoURL: repoURL, WebhookDelivery: "delivery-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "run-typed", Pipeline: "build", Status: "running", CreatedAt: now,
		Repo: "acme/widgets", RepoURL: repoURL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-typed", Pipeline: "build", Status: "running", StartedAt: now,
		DeclaredRepo: "acme/widgets", RepoURL: repoURL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-typed", NodeID: "compile", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-typed", "compile"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-remote", Pipeline: "build", Status: "running", StartedAt: now,
		DeclaredRepo: "acme/widgets", RepoURL: repoURL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-remote", NodeID: "compile", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-remote", "compile"); err != nil {
		t.Fatal(err)
	}

	ctrl := controller.New(st, nil).WithCacheCredentials(cache.URL, "cache-secret").EnableAuthFromStore()
	srv := httptest.NewServer(ctrl.Handler())
	defer srv.Close()
	for range 2 {
		claimed, cerr := client.NewWithToken(srv.URL, nil, owner).
			ClaimNode(ctx, "agent:workstation:1", nil, time.Minute, nil)
		if cerr != nil || claimed == nil {
			t.Fatalf("ClaimNode = %+v, %v", claimed, cerr)
		}
	}

	request := func(method, path, token, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	base := "/api/v1/runs/run-remote/gitcache/git"
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, base + "/register?name=" + cacheName + "&repo=" + repoURL, ""},
		{http.MethodGet, base + "/" + cacheName + "/info/refs?service=git-upload-pack", ""},
		{http.MethodPost, base + "/" + cacheName + "/git-upload-pack", "want"},
	} {
		resp := request(tc.method, tc.path, owner, tc.body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s status = %d, want 200", tc.method, tc.path, resp.StatusCode)
		}
	}
	for name, path := range map[string]string{
		"same-basename repository of another owner": base + "/register?name=" +
			sourceurl.ClaimedRepoNameFromURL("git@github.com:other/widgets.git") +
			"&repo=git@github.com:other/widgets.git",
		"foreign cache name": base + "/other/info/refs?service=git-upload-pack",
	} {
		t.Run(name, func(t *testing.T) {
			resp := request(http.MethodPost, path, owner, "")
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
	// bug: Old agents still read this route, while operator CLI runs have no webhook delivery.
	resp := request(http.MethodGet, "/api/v1/runs/run-typed/gitcache/git/"+
		cacheName+"/info/refs?service=git-upload-pack", owner, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the operator's CLI run reading its own source = %d, want 200", resp.StatusCode)
	}
	resp = request(http.MethodGet, base+"/"+cacheName+"/info/refs?service=git-upload-pack", stranger, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unclaimed runner status = %d, want 403", resp.StatusCode)
	}
	resp = request(http.MethodGet, "/api/v1/gitcache/git/widgets/info/refs?service=git-upload-pack", owner, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unscoped proxy status = %d, want 403", resp.StatusCode)
	}
	if len(cacheRequests) != 4 {
		t.Fatalf("cache requests = %v, want only the four claimed-source reads", cacheRequests)
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
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	server := httptest.NewUnstartedServer(controller.New(st, nil).WithCacheCredentials(cache.URL, "cache-secret").Handler())
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

// A run of a team other than the operator's opens none of the operator's
// mirrors, even of the repository the run names: the mirror's name is the
// URL's digest, the same for every team.
func TestGitcacheProxy_AnotherTeamsRunOpensNoMirror(t *testing.T) {
	repoURL := "git@github.com:victim/app.git"
	cacheName := sourceurl.ClaimedRepoNameFromURL(repoURL)
	var cacheRequests []string
	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cacheRequests = append(cacheRequests, r.Method+" "+r.URL.RequestURI())
		_, _ = w.Write([]byte("refs"))
	}))
	defer cache.Close()

	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.AsOperator().CreateTeam(ctx, "attacker"); err != nil {
		t.Fatal(err)
	}
	tenant, err := st.ForTeam(ctx, "attacker")
	if err != nil {
		t.Fatal(err)
	}
	runner, _, err := tenant.CreateToken(ctx, "attacker-runner", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.CreateTriggerWithRun(ctx, store.Trigger{
		ID: "run-attacker", Pipeline: "build", Status: "running", CreatedAt: now,
		Repo: "victim/app", RepoURL: repoURL, WebhookDelivery: "delivery-1",
	}, store.Run{
		ID: "run-attacker", Pipeline: "build", Status: "running", StartedAt: now,
		DeclaredRepo: "victim/app", RepoURL: repoURL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-attacker", NodeID: "compile", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-attacker", "compile"); err != nil {
		t.Fatal(err)
	}

	ctrl := controller.New(st, nil).WithCacheCredentials(cache.URL, "cache-secret").EnableAuthFromStore()
	srv := httptest.NewServer(ctrl.Handler())
	defer srv.Close()
	claimed, err := client.NewWithToken(srv.URL, nil, runner).
		ClaimNode(ctx, "agent:attacker-runner:1", nil, time.Minute, nil)
	if err != nil || claimed == nil {
		t.Fatalf("ClaimNode = %+v, %v", claimed, err)
	}
	req, err := http.NewRequest(http.MethodGet,
		srv.URL+"/api/v1/runs/run-attacker/gitcache/git/"+cacheName+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+runner)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another team's run reading the shared mirror = %d, want 403", resp.StatusCode)
	}
	if len(cacheRequests) != 0 {
		t.Fatalf("cache requests = %v, want none", cacheRequests)
	}
}
