//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func TestRunObservationProcessInterrupt(t *testing.T) {
	for _, verb := range []string{"logs", "list", "status"} {
		for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
			t.Run(fmt.Sprintf("%s/%s", verb, sig), func(t *testing.T) {
				ready := make(chan struct{})
				var once sync.Once
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { once.Do(func() { close(ready) }); <-r.Context().Done() }))
				defer server.Close()
				home := t.TempDir()
				config := filepath.Join(home, "config.yaml")
				if err := os.WriteFile(config, []byte(fmt.Sprintf("profiles:\n  fixture: {controller: {url: %s}}\n", server.URL)), 0o600); err != nil {
					t.Fatal(err)
				}
				binary := os.Getenv("SPARKWING_OBSERVE_TEST_CLI")
				if binary == "" {
					var err error
					binary, err = os.Executable()
					if err != nil {
						t.Fatal(err)
					}
				}
				args := []string{"runs", verb, "--profile", "fixture", "-o", "json"}
				if verb == "list" {
					args = append(args, "--wait")
				} else {
					args = append(args, "--run", "run", "--follow")
				}
				cmd := exec.CommandContext(t.Context(), binary, args...)
				cmd.Dir = home
				cmd.Env = append(os.Environ(), "HOME="+home, "SPARKWING_HOME="+filepath.Join(home, "state"), "SPARKWING_CONFIG="+config, "SPARKWING_PROFILE=")
				var stdout, stderr bytes.Buffer
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				exited := make(chan error, 1)
				go func() { exited <- cmd.Wait() }()
				finished := false
				defer func() {
					if !finished {
						_ = cmd.Process.Kill()
						<-exited
					}
				}()
				select {
				case <-ready:
				case err := <-exited:
					finished = true
					t.Fatalf("CLI ended before reaching its reader: %v: %s", err, stderr.String())
				}
				if err := cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				err := <-exited
				finished = true
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 3 {
					t.Fatalf("signal %s: want incomplete exit3, got %v; stderr=%s", sig, err, stderr.String())
				}
				var diagnostic struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(stderr.Bytes(), &diagnostic); err != nil || diagnostic.Error.Message == "" {
					t.Fatalf("missing structured diagnostic: %q: %v", stderr.String(), err)
				}
				if stdout.Len() != 0 {
					t.Fatalf("interrupted read invented output: %s", stdout.String())
				}
			})
		}
	}
}
