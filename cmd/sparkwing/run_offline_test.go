package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// A run's compile, binary-cache fill and exec stay on this machine even when
// the shell exports a profile, a secrets profile and controller URLs, and the
// pipeline process never sees the profile variables.
func TestCompileAndExec_ColdCacheContactsNoController(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: compiles a pipeline module; the fast class runs under -short")
	}
	if os.Getenv("SPARKWING_TEST_OFFLINE_CHILD") == "1" {
		err := compileAndExec(os.Getenv("SPARKWING_TEST_OFFLINE_DIR"), nil,
			append(os.Environ(), "SPARKWING_FLEET=1", "GOWORK=off"), compileOptions{NoUpdate: true})
		var cliErr *cliError
		if !errors.As(err, &cliErr) {
			fmt.Fprintf(os.Stderr, "compileAndExec: %v\n", err)
			os.Exit(19)
		}
		os.Exit(cliErr.code)
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
	main := "package main\nimport \"os\"\nfunc main() {\n" +
		"\tif os.Getenv(\"SPARKWING_PROFILE\") != \"\" || os.Getenv(\"SPARKWING_SECRETS_PROFILE\") != \"\" { os.Exit(18) }\n" +
		"\tos.Exit(17)\n}\n"
	if err := os.WriteFile(filepath.Join(moduleDir, "main.go"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("profiles:\n  remote:\n    controller: { url: %s, token: swu_test }\n", controller.URL)
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestCompileAndExec_ColdCacheContactsNoController$")
	cmd.Env = append(os.Environ(),
		"SPARKWING_TEST_OFFLINE_CHILD=1",
		"SPARKWING_TEST_OFFLINE_DIR="+moduleDir,
		"SPARKWING_HOME="+filepath.Join(t.TempDir(), "home"),
		"SPARKWING_CONFIG="+config,
		"SPARKWING_PROFILE=remote",
		"SPARKWING_SECRETS_PROFILE=remote",
		"SPARKWING_CONTROLLER_URL="+controller.URL,
		"SPARKWING_LOGS_URL="+controller.URL,
		"SPARKWING_GITCACHE_URL=",
		"GOWORK=off",
	)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child: %v\n%s", err, out)
	}
	switch exitErr.ExitCode() {
	case 17:
	case 18:
		t.Fatalf("the pipeline process inherited a profile variable\n%s", out)
	default:
		t.Fatalf("child exit %d, want the pipeline's 17\n%s", exitErr.ExitCode(), out)
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("a cold-cache run sent %d requests to the controller", n)
	}
}
