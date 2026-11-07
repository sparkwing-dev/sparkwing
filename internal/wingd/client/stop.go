package client

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const stopPoll = 25 * time.Millisecond

func Stop(ctx context.Context, opts Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sock, err := wingd.SocketPath(opts.Home)
	if err != nil {
		return err
	}
	cl := &Client{sock: sock, opts: opts}
	var ack wingwire.DrainAck
	path := controllerPath + "drain"
	if opts.RetireRunner {
		path += "?runner=retire"
	}
	if err := cl.apiCall(ctx, http.MethodPost, path, nil, &ack); err != nil {
		if dialMeansAbsent(err) {
			return ErrNoDaemon
		}
		return fmt.Errorf("wingd/client: stop daemon: drain not acknowledged: %w", err)
	}
	if ack.HoldersRemaining < 0 {
		return fmt.Errorf("wingd/client: invalid drain acknowledgement")
	}
	if err := waitSocketQuiet(ctx, sock, opts.dialTimeout()); err != nil {
		return err
	}
	return waitLockFree(ctx, opts.Home)
}

func waitSocketQuiet(ctx context.Context, sock string, timeout time.Duration) error {
	for {
		nc, err := dial(ctx, sock, timeout)
		if err != nil {
			return nil
		}
		_ = nc.Close()
		if err := sleep(ctx, stopPoll); err != nil {
			return fmt.Errorf("wingd/client: daemon on %s did not exit after draining: %w", sock, err)
		}
	}
}

func waitLockFree(ctx context.Context, home string) error {
	for {
		held, err := wingd.LockHeld(home)
		if err != nil {
			return fmt.Errorf("wingd/client: stop daemon: %w", err)
		}
		if !held {
			return nil
		}
		if err := sleep(ctx, stopPoll); err != nil {
			return fmt.Errorf("wingd/client: daemon for home %s still held its election lock after draining: %w", home, err)
		}
	}
}
