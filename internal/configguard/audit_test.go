package configguard_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/configguard"
)

// auditEnabled opts a run into the full-suite audit. It is off by
// default because the audit runs `go test ./...` inside a test, which
// doubles the wall time of the pre-commit gate. The invariant it checks
// is enforced structurally by paths.UnderTest and repos.DefaultPath (see
// the sandbox tests beside internal/paths and internal/repos); the audit
// is the end-to-end proof you reach for when you change how a home
// directory is resolved.
const auditEnabled = "SPARKWING_LIVE_CONFIG_AUDIT"

// auditInner marks the child `go test ./...` so it does not recurse.
const auditInner = "SPARKWING_LIVE_CONFIG_AUDIT_INNER"

// TestSuiteLeavesTheLiveConfigAlone is BW-1457 stated as a check: run
// the whole suite and prove it did not write the developer's sparkwing
// configuration. repos.yaml is the file that was found corrupt.
//
// It asserts two ways, because neither is sufficient alone. The
// sandbox-home run is the sound one: HOME points at an empty directory,
// so anything sparkwing writes there is a write that would have landed
// in the real home, and no daemon or parallel commit on the machine can
// forge it. The byte comparison of the real repos.yaml is the criterion
// as written, and it is kept because the sandbox run cannot see a writer
// that resolves the registry from something other than HOME.
//
// Run it with:
//
//	SPARKWING_LIVE_CONFIG_AUDIT=1 go test -timeout 30m ./internal/configguard/
func TestSuiteLeavesTheLiveConfigAlone(t *testing.T) {
	if os.Getenv(auditInner) == "1" {
		t.Skip("inner run of the audit; the outer process does the comparing")
	}
	if os.Getenv(auditEnabled) != "1" {
		t.Skipf("set %s=1 to run the full-suite audit", auditEnabled)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	root := repoRoot(t)
	sandbox := t.TempDir()

	before, err := configguard.Fingerprint(home)
	if err != nil {
		t.Fatalf("fingerprint before: %v", err)
	}

	out, runErr := runSuite(t, root, sandbox)

	after, err := configguard.Fingerprint(home)
	if err != nil {
		t.Fatalf("fingerprint after: %v", err)
	}

	leaks, err := configguard.SandboxLeaks(sandbox)
	if err != nil {
		t.Fatalf("scan sandbox: %v", err)
	}
	if len(leaks) > 0 {
		for i, p := range leaks {
			leaks[i] = strings.TrimPrefix(p, sandbox)
		}
		t.Errorf("the suite wrote sparkwing configuration into the home it was given:\n  %s",
			strings.Join(leaks, "\n  "))
	}
	if changed := configguard.Diff(before, after); len(changed) > 0 {
		t.Errorf("the suite modified the live sparkwing configuration:\n  %s",
			strings.Join(changed, "\n  "))
	}
	if runErr != nil {
		t.Logf("inner suite failed (%v); the assertions above still hold:\n%s", runErr, tail(string(out)))
	}
}

// runSuite runs the module's tests with HOME redirected at sandbox.
//
// The Go toolchain is invoked by absolute path and handed its caches
// explicitly because a version manager puts `go` behind a shim in the
// real home, and moving HOME out from under that shim makes it
// unrunnable.
func runSuite(t *testing.T, root, sandbox string) ([]byte, error) {
	t.Helper()
	goBin := filepath.Join(goEnv(t, root, "GOROOT"), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		t.Skipf("no go binary at %s: %v", goBin, err)
	}

	cmd := exec.Command(goBin, "test", "-count=1", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"HOME="+sandbox,
		auditInner+"=1",
		"GOCACHE="+goEnv(t, root, "GOCACHE"),
		"GOMODCACHE="+goEnv(t, root, "GOMODCACHE"),
		"GOPATH="+goEnv(t, root, "GOPATH"),
	)
	return cmd.CombinedOutput()
}

// goEnv reads one `go env` value using the ambient environment, before
// HOME is redirected.
func goEnv(t *testing.T, root, name string) string {
	t.Helper()
	cmd := exec.Command("go", "env", name)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env %s: %v", name, err)
	}
	return strings.TrimSpace(string(out))
}

// repoRoot walks up from the working directory to the directory holding
// go.mod, which is where `go test ./...` has to run from to cover the
// whole module.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// tail keeps a failure log readable by returning only its last lines.
func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n")
}
