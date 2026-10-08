//go:build !windows

package wingd

import "testing"

func TestUnixSocketHomeIdentityPreservesExactBytes(t *testing.T) {
	home := "left/../right"
	socket, err := SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	base := testSocketBase(t)
	if want := socketPathIn(base, home); socket != want {
		t.Fatalf("Unix home bytes changed socket identity: %q, want %q", socket, want)
	}
	if socket == socketPathIn(base, "right") {
		t.Fatal("Unix socket identity collapsed different home spellings")
	}
}
