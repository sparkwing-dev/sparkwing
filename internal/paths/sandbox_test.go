package paths

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testhome"
)

func TestDefaultPaths_FollowsHomeWithoutSparkwingHome(t *testing.T) {
	home := t.TempDir()
	testhome.Set(t, home)
	t.Setenv("SPARKWING_HOME", "")

	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if want := filepath.Join(home, ".sparkwing"); p.Root != want {
		t.Errorf("DefaultPaths root = %q, want %q", p.Root, want)
	}
	if root, ok := SandboxHome(); ok {
		t.Errorf("SandboxHome() = %q, true for the home's own ~/.sparkwing", root)
	}
}

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
	if root, ok := SandboxHome(); !ok || root != want {
		t.Errorf("SandboxHome() = %q, %v; want %q, true for a redirected home", root, ok, want)
	}
}
