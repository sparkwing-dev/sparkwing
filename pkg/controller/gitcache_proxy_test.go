package controller_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestGitcacheProxy_SeedForwardsBundle(t *testing.T) {
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
		srv.URL+"/api/v1/gitcache/seed?repo=https://git.example.com/acme/widgets.git&sha="+sha,
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
