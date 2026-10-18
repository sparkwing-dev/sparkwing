package wingd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketDirCarriesASuffixOnlyTheHomeKnows(t *testing.T) {
	home := t.TempDir()
	if unbound, err := SocketPath(home); err != nil || !strings.HasPrefix(unbound, home) {
		t.Fatalf("a home no daemon has bound resolved to %q (%v), want a path inside the home", unbound, err)
	}
	sock, err := EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	_, identity, err := socketHomeIdentity(home)
	if err != nil {
		t.Fatal(err)
	}
	predictable := strings.TrimSuffix(filepath.Base(filepath.Dir(socketPathIn(testSocketBase(t), identity, ""))), "-")
	if dir := filepath.Base(filepath.Dir(sock)); !strings.HasPrefix(dir, predictable+"-") || len(dir) < len(predictable)+17 {
		t.Fatalf("socket directory %q has no unpredictable suffix beyond %q", dir, predictable)
	}
	again, err := SocketPath(home)
	if err != nil || again != sock {
		t.Fatalf("socket path changed between calls: %q then %q (%v)", sock, again, err)
	}
	if err := os.Remove(filepath.Join(home, "wingd", socketTokenFile)); err != nil {
		t.Fatalf("the suffix is not recorded in the home: %v", err)
	}
	if fresh, err := EnsureSocketPath(home); err != nil || fresh == sock {
		t.Fatalf("a new recorded suffix kept the old path %q (%v)", fresh, err)
	}
}

func recordedSocketToken(t *testing.T, home string) string {
	t.Helper()
	token, err := readSocketToken(filepath.Join(home, "wingd", socketTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	return token
}
