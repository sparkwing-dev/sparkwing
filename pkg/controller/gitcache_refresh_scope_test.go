package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A refresh makes the cache fetch a caller-named repository with the
// operator's cache credential, so only the operator may ask for one; a team
// owner's refresh never reaches the cache.
func TestGitcacheRefresh_OnlyTheOperatorReachesTheCache(t *testing.T) {
	var mu sync.Mutex
	var cacheAuth []string
	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cacheAuth = append(cacheAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(cache.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	raw, pub := multiTeamLicense(t)
	srv := controller.New(st, nil).EnableAuthFromStore().
		WithLicense(license.Resolve(raw, pub, time.Now(), nil)).
		WithCacheCredentials(cache.URL, "operator-cache-credential")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	alice, teamA := signUp(t, st, "alice")
	owner := session(t, st, alice, teamA)

	refresh := func(auth string) int {
		t.Helper()
		target := ts.URL + "/api/v1/gitcache/refresh?" + url.Values{"repo": {"https://github.com/example/private"}}.Encode()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if code := refresh(owner); code != http.StatusForbidden {
		t.Errorf("a team owner's refresh = %d, want 403", code)
	}
	mu.Lock()
	reached := len(cacheAuth)
	mu.Unlock()
	if reached != 0 {
		t.Errorf("a team owner's refresh reached the cache %d time(s) with %q", reached, cacheAuth)
	}

	if code := refresh("Bearer " + root); code != http.StatusOK {
		t.Errorf("the operator's refresh = %d, want 200", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(cacheAuth) != 1 || cacheAuth[0] != "Bearer operator-cache-credential" {
		t.Errorf("the operator's refresh reached the cache as %q, want one request with the cache credential", cacheAuth)
	}
}
