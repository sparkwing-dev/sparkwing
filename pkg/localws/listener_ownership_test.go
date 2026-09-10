package localws

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunClosesSuppliedListenerOnSetupFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	home := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(home, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), Options{Listener: listener, Home: home}); err == nil {
		t.Fatal("accepted invalid home")
	}
	tcp := listener.(*net.TCPListener)
	if err := tcp.SetDeadline(time.Now()); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	_, err = listener.Accept()
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("listener remained open: %v", err)
	}
}
