package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type RefreshResult struct {
	PreviousVersion string
	RunningVersion  string
	Restarted       bool
}

func RefreshRunning(ctx context.Context, opts Options) (RefreshResult, error) {
	return replaceRunning(ctx, opts, false)
}

// RestartRunning replaces an answering daemon even when it already serves the
// requested build. An absent daemon remains stopped.
func RestartRunning(ctx context.Context, opts Options) (RefreshResult, error) {
	return replaceRunning(ctx, opts, true)
}

func replaceRunning(ctx context.Context, opts Options, force bool) (RefreshResult, error) {
	sock, err := wingd.SocketPath(opts.Home)
	if err != nil {
		return RefreshResult{}, err
	}
	nc, err := dial(ctx, sock, opts.dialTimeout())
	if err != nil {
		if u := unreachable(sock, err); u != nil {
			return RefreshResult{}, u
		}
		return RefreshResult{}, ErrNoDaemon
	}
	cl := &Client{opts: opts, nc: nc, dec: newFrameReader(nc), sock: sock}
	stop := cl.cancelOnDone(ctx)
	defer stop()
	ack, err := cl.handshake(opts.Version)
	if err != nil {
		_ = cl.Close()
		return RefreshResult{}, fmt.Errorf("wingd/client: refresh handshake: %w", err)
	}
	result := RefreshResult{PreviousVersion: ack.BinaryVersion}
	if !force && ack.BinaryVersion == opts.Version && !ack.Draining {
		result.RunningVersion = ack.BinaryVersion
		_ = cl.Close()
		return result, nil
	}
	cl.ack = ack
	cl.takeover(ctx, opts)
	successor, err := EnsureDaemon(ctx, opts)
	if err != nil {
		return result, fmt.Errorf("wingd/client: refresh successor: %w", err)
	}
	defer func() { _ = successor.Close() }()
	result.RunningVersion = successor.DaemonVersion()
	result.Restarted = true
	if result.RunningVersion != opts.Version {
		return result, fmt.Errorf("wingd/client: refreshed daemon reports %s, want %s", result.RunningVersion, opts.Version)
	}
	return result, nil
}

// StopResult describes what StopRunning found and what it left behind.
type StopResult struct {
	// StoppedVersion is the build the daemon was serving when it was asked to
	// drain, empty when no daemon answered.
	StoppedVersion string

	// HoldersRemaining is how many admission holders the daemon still had when
	// it began draining.
	HoldersRemaining int

	// Stopped reports that nothing answers the admission socket any more.
	Stopped bool
}

// StopRunning drains an answering daemon and waits for its socket to go quiet.
// The supervisor exits with the worker it started, so nothing respawns, and no
// successor is launched. An absent daemon returns [ErrNoDaemon].
func StopRunning(ctx context.Context, opts Options) (StopResult, error) {
	sock, err := wingd.SocketPath(opts.Home)
	if err != nil {
		return StopResult{}, err
	}
	nc, err := dial(ctx, sock, opts.dialTimeout())
	if err != nil {
		if u := unreachable(sock, err); u != nil {
			return StopResult{}, u
		}
		return StopResult{}, ErrNoDaemon
	}
	cl := &Client{opts: opts, nc: nc, dec: newFrameReader(nc), sock: sock}
	release := cl.cancelOnDone(ctx)
	defer release()
	ack, err := cl.handshake(opts.Version)
	if err != nil {
		_ = cl.Close()
		return StopResult{}, fmt.Errorf("wingd/client: stop handshake: %w", err)
	}
	cl.ack = ack
	result := StopResult{StoppedVersion: ack.BinaryVersion}
	opts.logf("stopping daemon %s", ack.BinaryVersion)
	if err := cl.writeWithin(&wingwire.DrainRequest{}, opts.dialTimeout()); err != nil {
		_ = cl.Close()
		return result, fmt.Errorf("wingd/client: stop drain: %w", err)
	}
	if conn := cl.conn(); conn != nil {
		_ = conn.SetReadDeadline(time.Now().Add(opts.dialTimeout()))
	}
	if msg, readErr := cl.dec.read(); readErr == nil {
		if drained, ok := msg.(*wingwire.DrainAck); ok {
			result.HoldersRemaining = drained.HoldersRemaining
		}
	}
	cl.Close()
	result.Stopped = awaitQuiet(ctx, sock)
	return result, nil
}

func awaitQuiet(ctx context.Context, sock string) bool {
	for {
		if _, err := Probe(ctx, sock); errors.Is(err, ErrNoDaemon) {
			return true
		}
		if err := sleep(ctx, 100*time.Millisecond); err != nil {
			return false
		}
	}
}
