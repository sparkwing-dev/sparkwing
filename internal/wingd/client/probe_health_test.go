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
	home, _ := startHealthProbeServer(t, wingd.ProtocolMajor)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := HealthProbe(ctx, home); err != nil {
		t.Fatalf("health probe waited for queue-state work after a successful handshake: %v", err)
	}
}

func TestHealthProbeRequiresCompatibleProtocol(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		protocolMajor int
		wantErr       bool
	}{
		{name: "current", protocolMajor: wingd.ProtocolMajor},
		{name: "lower", protocolMajor: wingd.ProtocolMajor - 1, wantErr: true},
		{name: "higher", protocolMajor: wingd.ProtocolMajor + 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, sock := startHealthProbeServer(t, tt.protocolMajor)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := healthProbeOnce(ctx, sock)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("health probe with protocol %d error = %v, want incompatible protocol error", tt.protocolMajor, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("health probe with protocol %d: %v", tt.protocolMajor, err)
			}
		})
	}
}

func startHealthProbeServer(t *testing.T, protocolMajor int) (string, string) {
	t.Helper()
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
