//go:build linux || darwin

package wingd

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerPIDReadsTheConnectingProcess(t *testing.T) {
	sock := filepath.Join(shortSocketBase(t), "peer.sock")
	listener := listenAt(t, sock)
	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if pid, known := peerPID(server); !known || pid != os.Getpid() {
		t.Fatalf("peer PID = %d (known %v), want %d", pid, known, os.Getpid())
	}
}

func TestPeerPIDReportsUnavailableCredentials(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	if pid, known := peerPID(server); known || pid != 0 {
		t.Fatalf("pipe peer PID = %d (known %v), want unknown", pid, known)
	}
}
