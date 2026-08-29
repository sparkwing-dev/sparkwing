package cluster

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTriggerBinaryUploadUsesCacheTokenNotAgentToken(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CACHE_TOKEN", "cache-write-token")
	var uploadAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			http.NotFound(w, r)
		case http.MethodPut:
			uploadAuthorization = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	pipelineDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pipelineDir, "go.mod"), []byte("module example.com/pipeline\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pipelineDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary, err := triggerBuildOrFetchBinary(pipelineDir, TriggerLoopOptions{
		ControllerURL: "https://controller.example",
		GitcacheURL:   server.URL,
		Token:         "agent-admin-token",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	binary.release()
	if uploadAuthorization != "Bearer cache-write-token" {
		t.Fatalf("binary upload authorization = %q, want cache token", uploadAuthorization)
	}
	if uploadAuthorization == "Bearer agent-admin-token" {
		t.Fatal("binary upload received agent token")
	}
}
