package client

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// ErrNoDaemon is returned by [Query] when no daemon is running for the
// home; the queue is empty because there is nothing coordinating it.
var ErrNoDaemon = errors.New("wingd/client: no daemon running")

// ErrDaemonUnreachable reports that this process could not find out whether a
// daemon is there. The socket failed to answer for a reason that is not
// "nothing is listening" -- the path was blocked, the accept backlog was full,
// the dial timed out -- so the queue behind it is unknown rather than empty.
//
// It is a separate sentinel from [ErrNoDaemon] because collapsing the two is
// what let a blind `sparkwing queue` print the same empty answer an idle
// machine prints. A caller that treats them alike reports health while blind.
var ErrDaemonUnreachable = errors.New("wingd/client: could not reach the admission daemon")

// dialMeansAbsent reports whether a dial failure means nothing is listening:
// no socket file at the path (ENOENT), or a socket file nobody accepts on
// (ECONNREFUSED). Those two are what an idle machine looks like, and spawning
// a daemon is the right answer to them.
//
// Every other failure leaves the daemon's presence unknown. Permission denied
// says the caller cannot reach the path at all, and a full backlog or a
// timeout says something is there but did not answer -- neither is evidence
// of an empty queue.
func dialMeansAbsent(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}

// unreachable wraps a dial failure that is not an absence, so callers can tell
// "I could not look" from "nothing was there" with [errors.Is]. It returns nil
// for an absence, which is the caller's signal to keep reporting [ErrNoDaemon].
func unreachable(sock string, dialErr error) error {
	if dialErr == nil || dialMeansAbsent(dialErr) {
		return nil
	}
	return fmt.Errorf("%w at %s: %w", ErrDaemonUnreachable, sock, dialErr)
}

// QueueState asks the daemon for its current admission picture over this
// client's connection. It is read-only and creates no lease. Use it on a
// dedicated connection, not one already holding a lease.
//
// A daemon blink during the read is retried on a fresh connection, with
// backoff between attempts and a bounded number of them: a status read
// nothing depends on must report a daemon it cannot reach rather than
// keep asking.
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

// readQueueState runs one queue-state exchange. The third value is a transport
// error the caller recovers by reconnecting, so a daemon blink during a status
// read is retried against the fresh connection.
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

// Query connects read-only and returns the daemon's queue state without
// spawning a daemon. When nothing is listening it returns [ErrNoDaemon], so
// the caller can report an empty queue rather than start a server. When the
// socket could not be reached at all it returns [ErrDaemonUnreachable], which
// the caller must not render as an empty queue: that state is unknown, not
// idle.
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
