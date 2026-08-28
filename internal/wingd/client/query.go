package client

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

var ErrNoDaemon = errors.New("wingd/client: no daemon running")

var ErrDaemonUnreachable = errors.New("wingd/client: could not reach the admission daemon")

func dialMeansAbsent(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}

func unreachable(sock string, dialErr error) error {
	if dialErr == nil || dialMeansAbsent(dialErr) {
		return nil
	}
	return fmt.Errorf("%w at %s: %w", ErrDaemonUnreachable, sock, dialErr)
}

func (cl *Client) QueueState(ctx context.Context) (wingwire.QueueState, error) {
	stop := cl.cancelOnDone(ctx)
	defer stop()
	retry := newRetry("queue state", readOnlyRetryLimit)
	for {
		qs, terminal, transient := cl.readQueueState()
		if transient == nil {
			return qs, terminal
		}
		if err := retry.wait(ctx, transient); err != nil {
			return wingwire.QueueState{}, err
		}
		if rerr := cl.recoverConn(ctx); rerr != nil {
			return wingwire.QueueState{}, rerr
		}
	}
}

func (cl *Client) readQueueState() (qs wingwire.QueueState, terminal, transient error) {
	if err := cl.write(&wingwire.QueueState{}); err != nil {
		return wingwire.QueueState{}, nil, err
	}
	msg, err := cl.dec.read()
	if err != nil {
		return wingwire.QueueState{}, nil, err
	}
	got, ok := msg.(*wingwire.QueueState)
	if !ok {
		return wingwire.QueueState{}, fmt.Errorf("wingd/client: expected queue_state, got %T", msg), nil
	}
	return *got, nil, nil
}

func Query(ctx context.Context, opts Options) (wingwire.QueueState, error) {
	noSpawn := queryOptions(opts)
	cl, err := EnsureDaemon(ctx, noSpawn)
	if err != nil {
		if errors.Is(err, ErrDaemonUnreachable) {
			return wingwire.QueueState{}, err
		}
		if errors.Is(err, ErrNoDaemon) {
			return wingwire.QueueState{}, ErrNoDaemon
		}
		return wingwire.QueueState{}, err
	}
	defer cl.Close()
	return cl.QueueState(ctx)
}

func queryOptions(opts Options) Options {
	opts.Spawn = func(string, string) error { return ErrNoDaemon }
	opts.NoTakeover = true
	opts.healthProbe = true
	return opts
}
