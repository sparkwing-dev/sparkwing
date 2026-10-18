//go:build !windows

package wingd

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBindingRemovesThisUsersStaleSocketDirs(t *testing.T) {
	base := shortSocketBase(t)
	mkdir := func(name string) string {
		dir := filepath.Join(base, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	stale := mkdir(socketDirPrefix() + "aaaaaaaaaaaa-0123456789abcdef")
	ln, err := net.Listen("unix", filepath.Join(stale, "d.sock"))
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	legacy := mkdir(socketDirPrefix() + "bbbbbbbbbbbb")
	live := mkdir(socketDirPrefix() + "cccccccccccc-0123456789abcdef")
	alive, err := net.Listen("unix", filepath.Join(live, "d.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer alive.Close()
	data := mkdir(socketDirPrefix() + "dddddddddddd-0123456789abcdef")
	if err := os.WriteFile(filepath.Join(data, "notes"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := mkdir("sparkwing-0-eeeeeeeeeeee")
	fresh := mkdir(socketDirPrefix() + "999999999999-0123456789abcdef")
	old := time.Now().Add(-2 * staleSocketAge)
	for _, dir := range []string{stale, legacy, live, data} {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}

	current := filepath.Join(base, socketDirPrefix()+"ffffffffffff-0123456789abcdef", "d.sock")
	listener, err := bindSocket(current)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for dir, want := range map[string]bool{stale: false, legacy: false, live: true, data: true, other: true, fresh: true, filepath.Dir(current): true} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s present=%v, want %v", filepath.Base(dir), err == nil, want)
		}
	}
}
