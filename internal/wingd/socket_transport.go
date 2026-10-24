package wingd

import (
	"context"
	"io/fs"
	"net"
	"path/filepath"
	"time"
)

// DialSocket connects to the platform's private controller transport at path.
func DialSocket(ctx context.Context, path string, timeout time.Duration) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// safety: a home no daemon has bound has no socket, however long its path.
	if filepath.Base(filepath.Dir(path)) == unboundSocketDir {
		return nil, &net.OpError{Op: "dial", Net: "unix", Err: fs.ErrNotExist}
	}
	if err := ValidateSocketDir(path); err != nil {
		return nil, err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return dialSocket(ctx, path)
}

// ListenSocket binds the private transport and yields connections with verified peer identities.
func ListenSocket(path string) (net.Listener, error) {
	listener, err := bindSocket(path)
	if err != nil {
		return nil, err
	}
	return apiListener{Listener: listener, d: &Daemon{}}, nil
}
