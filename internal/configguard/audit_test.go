package configguard_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/configguard"
)

const auditEnabled = "SPARKWING_LIVE_CONFIG_AUDIT"

const auditInner = "SPARKWING_LIVE_CONFIG_AUDIT_INNER"

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

func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n")
}
