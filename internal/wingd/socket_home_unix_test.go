//go:build !windows

package wingd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnixSocketHomeIdentityPreservesExactBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "left"), 0o700); err != nil {
		t.Fatal(err)
	}
	home := root + "/left/../right"
	socket, err := EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	base, token := testSocketBase(t), recordedSocketToken(t, home)
	if want := socketPathIn(base, home, token); socket != want {
		t.Fatalf("Unix home bytes changed socket identity: %q, want %q", socket, want)
	}
	if socket == socketPathIn(base, root+"/right", token) {
		t.Fatal("Unix socket identity collapsed different home spellings")
	}
}
