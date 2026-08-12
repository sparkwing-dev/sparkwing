package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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
	// SoleRunUnderLoad is set when the daemon's liveness floor admitted this
	// run as the only one that could fit an externally loaded box, so the
	// caller can narrate that further runs will queue. ExternalCores is the
	// measured non-sparkwing load at grant time.
	SoleRunUnderLoad bool
	ExternalCores    float64
	guardComplete    atomic.Bool
	guardMu          sync.Mutex
	guardSent        bool
}

// Acquire submits an all-or-nothing admission request and blocks until
// the daemon grants it, returning the [Lease]. While queued it invokes
// onQueued (nil to ignore) with each position update. A terminal negative
// outcome -- fail, skip, cancel_others eviction, or a draining daemon --
// returns an [*AdmissionError]; a daemon cancel of the still-queued run
// (from `sparkwing runs cancel`) returns a [*CancelledError]. Cancelling
// ctx abandons the request and closes the connection.
func (cl *Client) Acquire(ctx context.Context, req wingwire.AdmissionRequest, onQueued func(wingwire.Queued)) (*Lease, error) {
	stop := cl.cancelOnDone(ctx)
	defer stop()
	retry := newRetry("acquire", 0)
	// safety: a queue position pushed by the daemon is proof this exchange is working, so it returns the pacing to its base the way a delivered frame does for a guard watch; without it a long queue wait inherits the backoff an earlier blink left behind.
	progressed := func(q wingwire.Queued) {
		retry.reset()
		if onQueued != nil {
			onQueued(q)
		}
	}
	for {
		if err := cl.write(&req); err != nil {
			if werr := retry.wait(ctx, err); werr != nil {
				return nil, werr
			}
			if rerr := cl.recoverConn(ctx); rerr != nil {
				return nil, rerr
			}
			continue
		}
		lease, terminal, transient := cl.readGrant(req, progressed)
		if transient == nil {
			return lease, terminal
		}
		if err := retry.wait(ctx, transient); err != nil {
			return nil, err
		}
		if rerr := cl.recoverConn(ctx); rerr != nil {
			return nil, rerr
		}
	}
}

// readGrant reads the admission event stream until a terminal outcome. It
// returns either a granted lease, a terminal admission error, or -- as the
// third value -- a transport error signalling the connection dropped mid-wait,
// which [Client.Acquire] recovers by reconnecting and re-submitting. A daemon
// blink while queued therefore never surfaces as a closed-connection error;
// the run keeps waiting across the restart. Position pushes are re-delivered
// to onQueued each time, which is idempotent for the caller's status line.
func (cl *Client) readGrant(req wingwire.AdmissionRequest, onQueued func(wingwire.Queued)) (lease *Lease, terminal, transient error) {
	for {
		msg, err := cl.dec.read()
		if err != nil {
			return nil, nil, err
		}
		switch m := msg.(type) {
		case *wingwire.Grant:
			return &Lease{cl: cl, RunID: m.RunID, Token: m.LeaseToken, Resources: m.Resources, Semaphores: m.Semaphores, SoleRunUnderLoad: m.SoleRunUnderLoad, ExternalCores: m.ExternalCores}, nil, nil
		case *wingwire.Queued:
			if onQueued != nil {
				onQueued(*m)
			}
		case *wingwire.Evicted:
			return nil, cl.admissionError(m), nil
		case *wingwire.Cancel:
			return nil, &CancelledError{Reason: m.Reason}, nil
		default:
			return nil, fmt.Errorf("wingd/client: unexpected %T while acquiring", msg), nil
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
	retry := newRetry("re-attach", 0)
	for {
		lease, terminal, transient := cl.readReattach(token)
		if transient == nil {
			return lease, terminal
		}
		if err := retry.wait(ctx, transient); err != nil {
			return nil, err
		}
		if rerr := cl.recoverConn(ctx); rerr != nil {
			return nil, rerr
		}
	}
}

// readReattach writes a re-attach and reads its answer once. The third value
// is a transport error the caller recovers by reconnecting; a rejected
// re-attach ([ErrReattachRejected]) is a terminal answer, not a transient one,
// since it means the grace window has genuinely closed.
func (cl *Client) readReattach(token string) (lease *Lease, terminal, transient error) {
	if err := cl.write(&wingwire.Reattach{LeaseToken: token}); err != nil {
		return nil, nil, err
	}
	msg, err := cl.dec.read()
	if err != nil {
		return nil, nil, err
	}
	switch m := msg.(type) {
	case *wingwire.Grant:
		return &Lease{cl: cl, RunID: m.RunID, Token: m.LeaseToken, Resources: m.Resources}, nil, nil
	case *wingwire.Evicted:
		return nil, ErrReattachRejected, nil
	default:
		return nil, fmt.Errorf("wingd/client: unexpected %T while re-attaching", msg), nil
	}
}

// Release returns the lease explicitly and closes the connection.
func (l *Lease) Release() error {
	_ = l.cl.write(&wingwire.Release{LeaseToken: l.Token})
	return l.cl.Close()
}

// CompleteGuard declares that the process session bound to this lease has
// stopped executing. The daemon verifies the session before releasing the
// claim and acknowledges only after the release is durable.
func (l *Lease) CompleteGuard() error {
	l.guardComplete.Store(true)
	l.guardMu.Lock()
	defer l.guardMu.Unlock()
	return l.sendGuardCompleteLocked()
}

func (l *Lease) sendGuardCompleteLocked() error {
	if l.guardSent {
		return nil
	}
	if err := l.cl.write(&wingwire.GuardComplete{LeaseToken: l.Token}); err != nil {
		return err
	}
	l.guardSent = true
	return nil
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
	l.WatchControl(onEvicted, nil)
}

// WatchControl is [Lease.Watch] that also delivers an operator cancel
// pushed by the daemon (from `sparkwing runs cancel`) to onCancel. Either
// callback may be nil. Like Watch it is the connection's sole reader and
// returns when the connection ends.
func (l *Lease) WatchControl(onEvicted func(wingwire.Evicted), onCancel func(wingwire.Cancel)) {
	l.WatchGuard(onEvicted, onCancel, nil)
}

// WatchGuard is [Lease.WatchControl] with a completion acknowledgement for a
// guarded lease. The acknowledgement callback runs after the daemon has
// durably released the guarded process session.
//
// A connection that keeps dropping is re-established with backoff, so a
// daemon that accepts and immediately closes cannot turn the watcher into
// a reconnect loop running at socket speed. The pacing returns to its
// base once a frame arrives, which is the only proof the watch is working
// again.
func (l *Lease) WatchGuard(onEvicted func(wingwire.Evicted), onCancel func(wingwire.Cancel), onComplete func()) {
	retry := newRetry("guard watch", 0)
	for {
		msg, err := l.cl.dec.read()
		if err != nil {
			if !l.cl.closed.Load() {
				if werr := retry.wait(context.Background(), err); werr != nil {
					return
				}
			}
			recovered, guardGone := l.recoverWatch()
			if !recovered {
				if guardGone && onComplete != nil {
					onComplete()
				}
				return
			}
			continue
		}
		retry.reset()
		switch m := msg.(type) {
		case *wingwire.Evicted:
			if onEvicted != nil {
				onEvicted(*m)
			}
		case *wingwire.Cancel:
			if onCancel != nil {
				onCancel(*m)
			}
		case *wingwire.GuardCompleteAck:
			if onComplete != nil {
				onComplete()
			}
			return
		}
	}
}

// recoverWatch reconnects a held lease's connection after a daemon blink and
// reclaims the lease by presenting its token, so a holder keeps watching for
// evictions and cancels across a restart. It returns false when the daemon
// does not come back or the reattach grace window has closed, in which case
// the watcher stops -- the lease is genuinely gone.
//
// Giving up is logged with its consequence, never silently: the run keeps
// executing, but from that point it holds no lease, sees no evictions, and
// cannot be cancelled through the daemon. A client whose Spawn cannot host a
// successor ([ErrNoDaemonHost]) lands here on any mid-run daemon death, so
// this is the line that explains an otherwise invisible loss of arbitration.
func (l *Lease) recoverWatch() (recovered, guardGone bool) {
	if l.cl.closed.Load() {
		// Not a diagnostic: this is how every lease ends. The caller closed
		// the connection deliberately, and the watch stopping is the
		// intended consequence, not a loss of arbitration to report.
		return false, false
	}
	l.guardMu.Lock()
	defer l.guardMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultReattachTimeout)
	defer cancel()
	if err := l.cl.connect(ctx); err != nil {
		l.cl.opts.logf("lease %s: daemon connection lost and not recovered (%v); run continues without eviction watch or daemon-side cancel", l.RunID, err)
		return false, false
	}
	_, terminal, transient := l.cl.readReattach(l.Token)
	if terminal != nil || transient != nil {
		l.cl.opts.logf("lease %s: reattach after daemon restart failed (%v); run continues without eviction watch or daemon-side cancel",
			l.RunID, errors.Join(terminal, transient))
		return false, l.guardComplete.Load() && errors.Is(terminal, ErrReattachRejected)
	}
	l.guardSent = false
	if l.guardComplete.Load() {
		return l.sendGuardCompleteLocked() == nil, false
	}
	return true, false
}

// CancelLease asks the daemon to cancel a local run it arbitrates, by run
// id. It returns whether the daemon knew the run and signalled it; a
// false return means the caller should fall back to the controller. Use
// it on a dedicated control connection, not one holding a lease.
func (cl *Client) CancelLease(ctx context.Context, runID string) (bool, error) {
	stop := cl.cancelOnDone(ctx)
	defer stop()
	retry := newRetry("cancel lease", 0)
	for {
		found, terminal, transient := cl.readCancelLease(runID)
		if transient == nil {
			return found, terminal
		}
		if err := retry.wait(ctx, transient); err != nil {
			return false, err
		}
		if rerr := cl.recoverConn(ctx); rerr != nil {
			return false, rerr
		}
	}
}

// readCancelLease runs one cancel-by-run-id exchange. The third value is a
// transport error the caller recovers by reconnecting, so a daemon blink
// during the cancel exchange is retried against the fresh connection rather
// than reported as a failed cancel.
func (cl *Client) readCancelLease(runID string) (found bool, terminal, transient error) {
	if err := cl.write(&wingwire.CancelLease{RunID: runID}); err != nil {
		return false, nil, err
	}
	msg, err := cl.dec.read()
	if err != nil {
		return false, nil, err
	}
	ack, ok := msg.(*wingwire.CancelLeaseAck)
	if !ok {
		return false, fmt.Errorf("wingd/client: expected cancel_lease_ack, got %T", msg), nil
	}
	return ack.Found, nil, nil
}

// cancelOnDone arranges for a blocked read to fail once ctx is cancelled,
// by setting a past read deadline. The returned stop cancels the watcher.
func (cl *Client) cancelOnDone(ctx context.Context) (stop func()) {
	nc := cl.nc
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = nc.SetReadDeadline(time.Now())
		case <-done:
		}
	}()
	return func() {
		close(done)
		_ = nc.SetReadDeadline(time.Time{})
	}
}
