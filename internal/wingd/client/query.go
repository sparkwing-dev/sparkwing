package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// ErrNoDaemon is returned by [Query] when no daemon is running for the
// home; the queue is empty because there is nothing coordinating it.
var ErrNoDaemon = errors.New("wingd/client: no daemon running")

// QueueState asks the daemon for its current admission picture over this
// client's connection. It is read-only and creates no lease. Use it on a
// dedicated connection, not one already holding a lease.
func (cl *Client) QueueState(ctx context.Context) (wingwire.QueueState, error) {
	stop := cl.cancelOnDone(ctx)
	defer stop()
	for {
		qs, terminal, transient := cl.readQueueState()
		if transient == nil {
			return qs, terminal
		}
		if rerr := cl.recoverConn(ctx); rerr != nil {
			return wingwire.QueueState{}, rerr
		}
	}
}

// readQueueState runs one queue-state exchange. The third value is a transport
// error the caller recovers by reconnecting, so a daemon blink during a status
// read is retried against the fresh connection.
func (cl *Client) readQueueState() (qs wingwire.QueueState, terminal error, transient error) {
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

// Query connects read-only and returns the daemon's queue state without
// spawning a daemon. When none is running it returns [ErrNoDaemon] so the
// caller can report an empty queue rather than start a server.
func Query(ctx context.Context, opts Options) (wingwire.QueueState, error) {
	noSpawn := opts
	noSpawn.Spawn = func(string, string) error { return ErrNoDaemon }
	cl, err := EnsureDaemon(ctx, noSpawn)
	if err != nil {
		if errors.Is(err, ErrNoDaemon) {
			return wingwire.QueueState{}, ErrNoDaemon
		}
		return wingwire.QueueState{}, err
	}
	defer cl.Close()
	return cl.QueueState(ctx)
}
