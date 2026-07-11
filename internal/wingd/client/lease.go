package client

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// Lease is a granted admission held open by the client's connection.
// Closing the connection, or calling [Lease.Release], returns it.
type Lease struct {
	cl        *Client
	RunID     string
	Token     string
	Resources wingwire.HostResources
	// Semaphores names the semaphores the lease holds; on a child
	// attach it is the parent lease's set.
	Semaphores []string
}

// Acquire submits an all-or-nothing admission request and blocks until
// the daemon grants it, returning the [Lease]. While queued it invokes
// onQueued (nil to ignore) with each position update. A terminal negative
// outcome -- fail, skip, cancel_others eviction, or a draining daemon --
// returns an [*AdmissionError]. Cancelling ctx abandons the request and
// closes the connection.
func (cl *Client) Acquire(ctx context.Context, req wingwire.AdmissionRequest, onQueued func(wingwire.Queued)) (*Lease, error) {
	stop := cl.cancelOnDone(ctx)
	defer stop()
	if err := cl.write(&req); err != nil {
		return nil, err
	}
	for {
		msg, err := cl.dec.read()
		if err != nil {
			return nil, err
		}
		switch m := msg.(type) {
		case *wingwire.Grant:
			return &Lease{cl: cl, RunID: m.RunID, Token: m.LeaseToken, Resources: m.Resources, Semaphores: m.Semaphores}, nil
		case *wingwire.Queued:
			if onQueued != nil {
				onQueued(*m)
			}
		case *wingwire.Evicted:
			return nil, &AdmissionError{Policy: m.Policy, Key: m.Key, SupersededBy: m.SupersededBy}
		default:
			return nil, fmt.Errorf("wingd/client: unexpected %T while acquiring", msg)
		}
	}
}

// Reattach reclaims a lease that survived a daemon restart or takeover by
// presenting its token within the grace window. It returns
// [ErrReattachRejected] when the lease is gone, in which case the caller
// should [Client.Acquire] afresh.
func (cl *Client) Reattach(ctx context.Context, token string) (*Lease, error) {
	stop := cl.cancelOnDone(ctx)
	defer stop()
	if err := cl.write(&wingwire.Reattach{LeaseToken: token}); err != nil {
		return nil, err
	}
	msg, err := cl.dec.read()
	if err != nil {
		return nil, err
	}
	switch m := msg.(type) {
	case *wingwire.Grant:
		return &Lease{cl: cl, RunID: m.RunID, Token: m.LeaseToken, Resources: m.Resources}, nil
	case *wingwire.Evicted:
		return nil, ErrReattachRejected
	default:
		return nil, fmt.Errorf("wingd/client: unexpected %T while re-attaching", msg)
	}
}

// Release returns the lease explicitly and closes the connection.
func (l *Lease) Release() error {
	_ = l.cl.write(&wingwire.Release{LeaseToken: l.Token})
	return l.cl.Close()
}

// Watch reads the held connection until it closes, invoking onEvicted
// when the daemon pushes an eviction (a cancel_others requester
// superseded this lease). It returns when the connection ends --
// [Lease.Release] and Close both terminate it -- so run it on its own
// goroutine for the lease's lifetime.
//
// safety: the connection has exactly one reader; after Acquire returns,
// Watch is that reader, so nothing else may read until it exits.
func (l *Lease) Watch(onEvicted func(wingwire.Evicted)) {
	for {
		msg, err := l.cl.dec.read()
		if err != nil {
			return
		}
		if ev, ok := msg.(*wingwire.Evicted); ok && onEvicted != nil {
			onEvicted(*ev)
		}
	}
}

// cancelOnDone arranges for a blocked read to fail once ctx is cancelled,
// by setting a past read deadline. The returned stop cancels the watcher.
func (cl *Client) cancelOnDone(ctx context.Context) (stop func()) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = cl.nc.SetReadDeadline(time.Now())
		case <-done:
		}
	}()
	return func() {
		close(done)
		_ = cl.nc.SetReadDeadline(time.Time{})
	}
}
