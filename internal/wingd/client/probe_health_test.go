package client

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestProbeCompletesAfterHandshake(t *testing.T) {
	t.Parallel()
	_, socket := startHealthProbeServer(t, wingd.ProtocolMajor)
	info, err := Probe(t.Context(), socket)
	if err != nil {
		t.Fatalf("probe waited for queue-state work after a successful handshake: %v", err)
	}
	if info.ProtocolMajor != wingd.ProtocolMajor || info.BinaryVersion != "test" {
		t.Fatalf("probe lost handshake status: %+v", info)
	}
}

func startHealthProbeServer(t *testing.T, protocolMajor int) (string, string) {
	t.Helper()
	home := shortHome(t)
	sock, err := wingd.EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := wingd.ListenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = listener.Close()
		close(release)
		<-done
	})
	go func() {
		defer close(done)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		reader := newFrameReader(conn)
		if _, readErr := reader.read(); readErr != nil {
			return
		}
		line, encodeErr := wingwire.Encode(&wingwire.HelloAck{
			ProtocolMajor: protocolMajor,
			BinaryVersion: "test",
		})
		if encodeErr != nil {
			return
		}
		if _, writeErr := conn.Write(line); writeErr != nil {
			return
		}
		<-release
	}()
	return home, sock
}
