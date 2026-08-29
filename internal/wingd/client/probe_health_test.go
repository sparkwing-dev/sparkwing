package client

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestHealthProbeCompletesAfterHandshake(t *testing.T) {
	t.Parallel()
	home := shortHome(t)
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", sock)
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
			ProtocolMajor: wingd.ProtocolMajor,
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

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := HealthProbe(ctx, home); err != nil {
		t.Fatalf("health probe waited for queue-state work after a successful handshake: %v", err)
	}
}
