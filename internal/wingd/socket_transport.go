package wingd

import (
	"context"
	"net"
	"time"
)

// DialSocket connects to the platform's private controller transport at path.
func DialSocket(ctx context.Context, path string, timeout time.Duration) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
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

// ListenSocket binds the private transport and yields authenticated APIConn connections.
func ListenSocket(path string) (net.Listener, error) {
	listener, err := bindSocket(path)
	if err != nil {
		return nil, err
	}
	return apiListener{Listener: listener, d: &Daemon{}}, nil
}
