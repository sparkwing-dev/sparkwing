package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultPaths_NeverResolvesToTheRealHomeUnderTest guarantees that a
// fixture which forgets SPARKWING_HOME gets a
// disposable sandbox, not the developer's ~/.sparkwing. The dispatch
// test in cmd/sparkwing was rewriting the real version stamp on every
// run before this landed.
func TestDefaultPaths_NeverResolvesToTheRealHomeUnderTest(t *testing.T) {
	t.Setenv("SPARKWING_HOME", "")

	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if p.Root == filepath.Join(home, ".sparkwing") {
		t.Fatalf("DefaultPaths resolved to the developer's own home %s", p.Root)
	}
	if !strings.HasPrefix(p.Root, os.TempDir()) {
		t.Errorf("DefaultPaths root = %q, want a path under %s", p.Root, os.TempDir())
	}
}

// TestDefaultPaths_StillHonorsSparkwingHome keeps the sandbox from
// overriding the isolation tests already do for themselves.
func TestDefaultPaths_StillHonorsSparkwingHome(t *testing.T) {
	want := t.TempDir()
	t.Setenv("SPARKWING_HOME", want)
	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if p.Root != want {
		t.Errorf("DefaultPaths root = %q, want the SPARKWING_HOME value %q", p.Root, want)
	}
}

// TestUnderTest holds the detection honest: this assertion runs inside a
// test binary, so it has to be true here, and the suffix it keys on is
// the one `go test` gives the binaries it builds.
func TestUnderTest(t *testing.T) {
	if !UnderTest() {
		t.Fatalf("UnderTest() is false inside a test binary; os.Args[0] = %q", os.Args[0])
	}
	if !strings.Contains(TestSandbox(), "sparkwing-test-home-") {
		t.Errorf("TestSandbox() = %q, want a pid-keyed sparkwing sandbox", TestSandbox())
	}
}
