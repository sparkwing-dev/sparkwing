package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func writeTrivialPipeline(t *testing.T) string {
	t.Helper()
	pipelineDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pipelineDir, "go.mod"), []byte("module example.com/pipeline\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pipelineDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return pipelineDir
}

// The launcher's bin cache traffic carries the run's grant, never the runner
// token and never an operator cache token left in the launcher's environment.
func TestTriggerBinaryCacheCarriesTheRunGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.6s of real work; the fast class runs under -short")
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CACHE_TOKEN", "operator-cache-token")
	var fetchAuthorization, uploadAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fetchAuthorization = r.Header.Get("Authorization")
			http.NotFound(w, r)
		case http.MethodPut:
			uploadAuthorization = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	binary, err := triggerBuildOrFetchBinary(context.Background(), writeTrivialPipeline(t), TriggerLoopOptions{
		ControllerURL: "https://controller.example",
		GitcacheURL:   server.URL,
		Token:         "runner-token",
	}, "swcg1.run-grant", nil)
	if err != nil {
		t.Fatal(err)
	}
	binary.release()
	for name, got := range map[string]string{"fetch": fetchAuthorization, "upload": uploadAuthorization} {
		if got != "Bearer swcg1.run-grant" {
			t.Errorf("binary %s authorization = %q, want the run's grant", name, got)
		}
	}
}

// With no grant the launcher skips the bin cache outright rather than falling
// back to whatever cache token its own environment holds.
func TestTriggerBinaryCacheIsSkippedWithoutAGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.6s of real work; the fast class runs under -short")
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CACHE_TOKEN", "operator-cache-token")
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer server.Close()

	binary, err := triggerBuildOrFetchBinary(context.Background(), writeTrivialPipeline(t), TriggerLoopOptions{
		ControllerURL: "https://controller.example",
		GitcacheURL:   server.URL,
		Token:         "runner-token",
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	binary.release()
	if binary.cache != binaryCacheCompiled {
		t.Errorf("binary cache outcome = %q, want %q", binary.cache, binaryCacheCompiled)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the cache saw %d requests from a run that holds no grant", n)
	}
}
