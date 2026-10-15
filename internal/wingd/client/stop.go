package client

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const stopPoll = 25 * time.Millisecond

func Stop(ctx context.Context, opts Options) error {
	nc, sock, err := dialLocalController(ctx, opts.Home, opts.dialTimeout())
	if err != nil {
		return err
	}
	defer nc.Close()
	if err := exchangeLocalDrain(ctx, nc, sock); err != nil {
		return err
	}
	if err := waitSocketQuiet(ctx, sock, opts.dialTimeout()); err != nil {
		return err
	}
	return waitLockFree(ctx, opts.Home)
}

func exchangeLocalDrain(ctx context.Context, nc net.Conn, sock string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	cl := &Client{nc: nc, dec: newFrameReader(nc), sock: sock}
	// safety: a read deadline lets context cancellation interrupt a daemon that
	// accepts but never responds.
	unwatch := cl.cancelOnDone(ctx)
	defer unwatch()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	// safety: the daemon answers nothing before a hello, so the handshake has to precede the drain frame.
	ack, err := cl.handshake("")
	if err != nil {
		return fmt.Errorf("wingd/client: stop daemon: handshake: %w", err)
	}
	if ack.ProtocolMajor != wingwire.ProtocolMajor {
		return fmt.Errorf("wingd/client: stop daemon: protocol %d differs from %d", ack.ProtocolMajor, wingwire.ProtocolMajor)
	}
	if err := cl.write(&wingwire.DrainRequest{}); err != nil {
		return fmt.Errorf("wingd/client: stop daemon: %w", err)
	}
	msg, err := cl.dec.read()
	if err != nil {
		return fmt.Errorf("wingd/client: stop daemon: drain not acknowledged: %w", err)
	}
	if refusal := cl.lacksOperation(msg); refusal != nil {
		return fmt.Errorf("wingd/client: stop daemon: %w", refusal)
	}
	acknowledged, ok := msg.(*wingwire.DrainAck)
	if !ok || acknowledged.HoldersRemaining < 0 {
		return fmt.Errorf("wingd/client: stop daemon: invalid drain acknowledgement %T", msg)
	}
	return nil
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
