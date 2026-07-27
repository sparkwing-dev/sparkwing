package client

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// stopPoll is how often [Stop] re-dials while waiting for a drained
// daemon's process to let go of its socket.
const stopPoll = 25 * time.Millisecond

// Stop drains the daemon serving opts.Home and waits until nothing
// answers its socket, so the caller can rely on the home being daemon-free
// when it returns. It returns [ErrNoDaemon] when none was running.
//
// It never spawns, never hands off, and never takes over: a caller asking
// for the daemon to be gone must not be left with a fresh one in its
// place. Callers that own a daemon's lifetime -- a test fixture, an
// operator clearing a version skew -- want this rather than the idle
// timeout, which is minutes long and leaves a stray daemon looking like
// the resident one in the meantime.
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
	// safety: a daemon that accepts but never answers would otherwise block forever; this hands ctx the only lever over a raw read.
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
	return waitSocketQuiet(ctx, sock, opts.dialTimeout())
}

// waitSocketQuiet blocks until nothing accepts on sock, or ctx ends. A
// drained daemon closes its listener and exits, so an answering socket
// means the process is still on its way down.
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
