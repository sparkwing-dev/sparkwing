//go:build unix

package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

// safety: only the built CLI and generated source are shared. Every test
// still supplies its own home, state backend, probes, and run IDs.
var (
	e2eFixtureOnce sync.Once
	e2eFixtureCLI  string
)

// safety: the CLI is built once, under a root TestMain removes after every test.
func e2eFixture(t *testing.T) (root, cli string) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the sparkwing CLI; run without -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	e2eFixtureOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sparkwing-e2e-")
		if err != nil {
			t.Fatalf("create e2e fixture root: %v", err)
		}
		e2eFixtureRoot = dir
		e2eFixtureCLI = filepath.Join(dir, "sparkwing")
		runGo(t, repoRootDir(t), append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local"), "build", "-o", e2eFixtureCLI, "./cmd/sparkwing")
	})
	return e2eFixtureRoot, e2eFixtureCLI
}

const daemonStopTimeout = 15 * time.Second

func stopHomeDaemon(t *testing.T, home string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), daemonStopTimeout)
		defer cancel()
		err := wingdclient.Stop(ctx, wingdclient.Options{Home: home})
		if err == nil || errors.Is(err, wingdclient.ErrNoDaemon) {
			return
		}
		t.Errorf("admission daemon for %s outlived the test: %v", home, err)
	})
}

// safety: a reproducible build trims the compiled-in source path, so resolving the
// repository from runtime.Caller yields a module path the filesystem does not hold.
// This resolves at package initialization because tests in this package change the
// working directory, and a later walk would start from wherever one of them left it.
var moduleRootAtInit, moduleRootInitErr = func() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = parent
	}
}()

func repoRootDir(t *testing.T) string {
	t.Helper()
	if moduleRootInitErr != nil {
		t.Fatalf("resolve repository root: %v", moduleRootInitErr)
	}
	return moduleRootAtInit
}

func writeMod(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGo(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}
