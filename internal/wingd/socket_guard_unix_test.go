//go:build !windows

package wingd

import (
	"io/fs"
	"net"
	"syscall"
	"testing"
)

type foreignOwner struct {
	fs.FileInfo
}

func (f foreignOwner) Sys() any {
	stat, ok := f.FileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return f.FileInfo.Sys()
	}
	other := *stat
	other.Uid++
	return &other
}

func listenAt(t *testing.T, sock string) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}
