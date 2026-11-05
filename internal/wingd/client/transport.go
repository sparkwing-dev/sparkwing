package client

import (
	"context"
	"net"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

const maxFrame = 8 << 20

func dial(ctx context.Context, sock string, timeout time.Duration) (net.Conn, error) {
	return wingd.DialSocket(ctx, sock, timeout)
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
