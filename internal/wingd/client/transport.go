package client

import (
	"bufio"
	"context"
	"net"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const maxFrame = 8 << 20

// frameReader decodes newline-delimited wingwire frames off a connection.
type frameReader struct {
	sc *bufio.Scanner
}

func newFrameReader(nc net.Conn) *frameReader {
	sc := bufio.NewScanner(nc)
	sc.Buffer(make([]byte, 0, 64<<10), maxFrame)
	return &frameReader{sc: sc}
}

func (r *frameReader) read() (wingwire.Message, error) {
	if !r.sc.Scan() {
		if err := r.sc.Err(); err != nil {
			return nil, err
		}
		return nil, net.ErrClosed
	}
	return wingwire.Decode(r.sc.Bytes())
}

func dial(ctx context.Context, sock string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	return d.DialContext(ctx, "unix", sock)
}

// sleep waits d or returns early if ctx is cancelled.
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
