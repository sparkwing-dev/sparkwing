//go:build !windows

package client

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestDial_RefusesAForeignSocketDirectory(t *testing.T) {
	home := shortHome(t)
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chmod(dir, 0o700)
		_ = os.RemoveAll(dir)
	}()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	// safety: a directory this user cannot write stands in for one another
	// account planted, which only root can create in a test.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}

	nc, derr := dial(context.Background(), sock, time.Second)
	if derr == nil {
		_ = nc.Close()
		t.Fatal("dial handed a connection to a listener in a foreign socket directory")
	}
	if !strings.Contains(derr.Error(), dir) {
		t.Errorf("refusal %v does not name the directory %q", derr, dir)
	}
}
