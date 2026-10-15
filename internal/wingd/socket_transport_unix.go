//go:build !windows

package wingd

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func bindSocket(path string) (net.Listener, error) {
	if err := ValidateSocketPath(path); err != nil {
		return nil, err
	}
	if err := ensureSocketDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
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
