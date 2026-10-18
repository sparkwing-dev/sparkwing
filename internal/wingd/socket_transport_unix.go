//go:build !windows

package wingd

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func bindSocket(path string) (net.Listener, error) {
	if err := ValidateSocketPath(path); err != nil {
		return nil, err
	}
	if err := ensureSocketDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	removeStaleSocketDirs(filepath.Dir(path))
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("wingd: listen %s: %w", path, err)
	}
	// safety: the process umask can leave a new socket connectable by other accounts.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("wingd: restrict socket %s: %w", path, err)
	}
	return ln, nil
}

func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", path)
}

func socketPresent(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

// safety: only this account's directories that hold nothing but sockets no
// daemon answers on are removed, so a live daemon or stray data is never lost.
func removeStaleSocketDirs(keep string) {
	base := filepath.Dir(keep)
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, entry := range entries {
		dir := filepath.Join(base, entry.Name())
		if dir == keep || !entry.IsDir() || !strings.HasPrefix(entry.Name(), socketDirPrefix()) || checkSocketDir(dir) != nil {
			continue
		}
		// safety: another home's daemon may have just created its directory and not yet bound.
		if info, err := entry.Info(); err != nil || time.Since(info.ModTime()) < staleSocketAge {
			continue
		}
		if staleSocketDir(dir) {
			_ = os.RemoveAll(dir)
		}
	}
}

const staleSocketAge = time.Minute

func staleSocketDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if !socketPresent(path) {
			return false
		}
		if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = conn.Close()
			return false
		}
	}
	return true
}
