package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestServeTokenRefusesEscapingSymlink(t *testing.T) {
	p := PathsAt(t.TempDir())
	target := filepath.Join(t.TempDir(), "token")
	if err := fssecure.WriteFile(target, []byte(strings.Repeat("a", 64))); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p.ServeTokenFile()); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if got, err := p.ServeToken(); err == nil || got != "" {
		t.Fatalf("escaped serve token = %q, %v", got, err)
	}
}
