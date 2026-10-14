package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// A run's compile and binary-cache fill stay on this machine even when the
// shell exports a profile, a secrets profile and controller URLs.
func TestMaterialize_ColdCacheContactsNoController(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: compiles a pipeline module; the fast class runs under -short")
	}
	if os.Getenv("SPARKWING_TEST_OFFLINE_CHILD") == "1" {
		if err := materializeOnly(os.Getenv("SPARKWING_TEST_OFFLINE_DIR"),
			append(os.Environ(), "SPARKWING_FLEET=1", "GOWORK=off"), compileOptions{NoUpdate: true}); err != nil {
			fmt.Fprintf(os.Stderr, "materialize: %v\n", err)
			os.Exit(19)
		}
		os.Exit(0)
	}

	var requests atomic.Int32
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer controller.Close()

	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.test/offline\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("profiles:\n  remote:\n    controller: { url: %s, token: swu_test }\n", controller.URL)
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestMaterialize_ColdCacheContactsNoController$")
	cmd.Env = append(os.Environ(),
		"SPARKWING_TEST_OFFLINE_CHILD=1",
		"SPARKWING_TEST_OFFLINE_DIR="+moduleDir,
		"SPARKWING_HOME="+filepath.Join(t.TempDir(), "home"),
		"SPARKWING_CONFIG="+config,
		"SPARKWING_CONTROLLER_URL="+controller.URL,
		"SPARKWING_GITCACHE_URL=",
		"GOWORK=off",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("a cold-cache run sent %d requests to the controller", n)
	}
}
