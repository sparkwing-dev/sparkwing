package client

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const stopPoll = 25 * time.Millisecond

func Stop(ctx context.Context, opts Options) error {
	sock, err := wingd.SocketPath(opts.Home)
	if err != nil {
		return err
	}
	nc, err := dial(ctx, sock, opts.dialTimeout())
	if err != nil {
		return ErrNoDaemon
	}
	cl := &Client{opts: opts, nc: nc, dec: newFrameReader(nc), sock: sock}
	defer func() { _ = cl.Close() }()
	// safety: a read deadline lets context cancellation interrupt a daemon that
	// accepts but never responds.
	unwatch := cl.cancelOnDone(ctx)
	defer unwatch()
	// safety: the daemon answers nothing before a hello, so the handshake has to precede the drain frame.
	if _, err := cl.handshake(""); err != nil {
		return fmt.Errorf("wingd/client: stop daemon: handshake: %w", err)
	}
	if err := cl.write(&wingwire.DrainRequest{}); err != nil {
		return fmt.Errorf("wingd/client: stop daemon: %w", err)
	}
	if _, err := cl.dec.read(); err != nil {
		return fmt.Errorf("wingd/client: stop daemon: drain not acknowledged: %w", err)
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
