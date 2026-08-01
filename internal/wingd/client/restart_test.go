package client

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestRefreshRunning_ContextBoundsHandshake(t *testing.T) {
	home := shortHome(t)
	socket, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = RefreshRunning(ctx, Options{Home: home, Version: "v0.22.2-dev+12345678"})
	if err == nil {
		t.Fatal("refresh succeeded against a daemon that never answered")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("refresh ignored its context for %s", elapsed)
	}
	select {
	case conn := <-accepted:
		_ = conn.Close()
	default:
	}
}
