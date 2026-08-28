package client

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type RefreshResult struct {
	PreviousVersion string
	RunningVersion  string
	Restarted       bool
}

func RefreshRunning(ctx context.Context, opts Options) (RefreshResult, error) {
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
	if ack.BinaryVersion == opts.Version && !ack.Draining {
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
