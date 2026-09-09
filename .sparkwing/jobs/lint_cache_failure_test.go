package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestLintRestoreFailureStopsExecution(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		status int
		body   string
	}{
		{"server failure", http.StatusInternalServerError, "failed"},
		{"corrupt archive", http.StatusOK, "invalid archive"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			lintFixtureRepo(t)
			t.Setenv("TMPDIR", t.TempDir())
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(testCase.status)
				_, _ = response.Write([]byte(testCase.body))
			}))
			t.Cleanup(server.Close)
			t.Setenv("SPARKWING_GITCACHE_URL", server.URL)
			err := runGolangciLint(context.Background())
			if err == nil || !strings.Contains(err.Error(), "restore") {
				t.Errorf("lint error = %v, want cache restore failure", err)
			}
			entries, readErr := os.ReadDir(sparkwing.ToolCacheDir("golangci-lint"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("lint populated its cache after restore failure: %v", entries)
			}
		})
	}
}

func TestLintSaveFailureFailsGate(t *testing.T) {
	lintFixtureRepo(t)
	t.Setenv("TMPDIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		response.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	t.Setenv("SPARKWING_GITCACHE_URL", server.URL)
	cache := sparkwing.ToolCacheDir("golangci-lint")
	if err := os.WriteFile(filepath.Join(cache, "fixture-cache"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runGolangciLint(context.Background())
	if err == nil || !strings.Contains(err.Error(), "save") {
		t.Fatalf("lint error = %v, want cache save failure", err)
	}
}
