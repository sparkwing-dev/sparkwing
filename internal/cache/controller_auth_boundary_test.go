package cache

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestCacheBearerAuthenticatesOnlyToTheCache(t *testing.T) {
	cacheBearer := "sws_" + strings.Repeat("c", 48)
	t.Setenv("SPARKWING_CACHE_TOKEN", cacheBearer)
	cacheServer := newTestServer(t, os.Getenv("SPARKWING_CACHE_TOKEN"))

	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	controllerBearer, _, err := state.CreateToken("runner-pool", store.TokenKindService,
		[]string{controller.ScopeRunsRead}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	controllerServer := httptest.NewServer(controller.New(state, nil).EnableAuthFromStore().Handler())
	t.Cleanup(controllerServer.Close)

	request := func(base, path, bearer string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if got := request(cacheServer.URL, "/repos", cacheBearer); got != http.StatusOK {
		t.Fatalf("cache bearer at cache = %d, want 200", got)
	}
	if got := request(cacheServer.URL, "/repos", controllerBearer); got != http.StatusUnauthorized {
		t.Fatalf("controller bearer at cache = %d, want 401", got)
	}
	if got := request(controllerServer.URL, "/api/v1/agents", cacheBearer); got != http.StatusUnauthorized {
		t.Fatalf("cache bearer at controller = %d, want 401", got)
	}
	if got := request(controllerServer.URL, "/api/v1/agents", controllerBearer); got != http.StatusOK {
		t.Fatalf("controller bearer at controller = %d, want 200", got)
	}
}
