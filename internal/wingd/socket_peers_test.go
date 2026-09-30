//go:build linux

package wingd

import (
	"os"
	"slices"
	"syscall"
	"testing"
)

// A starting daemon's socket exists between bind and listen and refuses a
// dial there, exactly as a killed daemon's does. Discovery that unlinked what
// it found dead deleted the socket before the daemon could chmod it.
func TestPeerSockets_LeavesSocketsItCannotReachInPlace(t *testing.T) {
	starting := writeSocketPlaceholder(t, mustSocketPath(t, t.TempDir()))
	if err := os.Remove(starting); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: starting}); err != nil {
		t.Fatal(err)
	}
	killed := writeSocketPlaceholder(t, mustSocketPath(t, t.TempDir()))

	peers, err := PeerSockets(t.TempDir())
	if err != nil {
		t.Fatalf("peer sockets: %v", err)
	}
	for _, sock := range []string{starting, killed} {
		if slices.Contains(peers, sock) {
			t.Errorf("unreachable socket %q returned as a peer", sock)
		}
		if _, err := os.Lstat(sock); err != nil {
			t.Errorf("discovery removed %q: %v", sock, err)
		}
	}
}

func mustSocketPath(t *testing.T, home string) string {
	t.Helper()
	sock, err := SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	return sock
}
