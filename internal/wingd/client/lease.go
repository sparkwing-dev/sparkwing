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

type Lease struct {
	cl        *Client
	RunID     string
	Token     string
	Resources wingwire.HostResources

	Semaphores []string

	SoleRunUnderLoad bool
	ExternalCores    float64
	guardComplete    atomic.Bool
	guardMu          sync.Mutex
	guardSent        bool
}

func (cl *Client) Acquire(ctx context.Context, req wingwire.AdmissionRequest, onQueued func(wingwire.Queued)) (*Lease, error) {
	stop := cl.cancelOnDone(ctx)
	defer stop()
	retry := newRetry("acquire", 0)
	// safety: a queue update proves the exchange is healthy; reset retry pacing
	// so a long wait does not inherit backoff from an earlier disconnect.
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
		case *wingwire.Unsupported:
			return nil, daemonLacksOperation(m.Type, cl.ack.BinaryVersion), nil
		default:
			return nil, fmt.Errorf("wingd/client: unexpected %T while acquiring", msg), nil
		}
	}
}

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
	case *wingwire.Unsupported:
		return nil, daemonLacksOperation(m.Type, cl.ack.BinaryVersion), nil
	default:
		return nil, fmt.Errorf("wingd/client: unexpected %T while re-attaching", msg), nil
	}
}

func (l *Lease) Release() error {
	_ = l.cl.write(&wingwire.Release{LeaseToken: l.Token})
	return l.cl.Close()
}

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
// again. It returns [ErrReattachRejected] when a successor no longer has
// the guarded lease, or the recovery error that otherwise ended the watch.
func (l *Lease) WatchGuard(onEvicted func(wingwire.Evicted), onCancel func(wingwire.Cancel), onComplete func()) error {
	retry := newRetry("guard watch", 0)
	for {
		msg, err := l.cl.dec.read()
		if err != nil {
			if !l.cl.closed.Load() {
				if werr := retry.wait(context.Background(), err); werr != nil {
					return werr
				}
			}
			recovered, guardGone, recoverErr := l.recoverWatch()
			if !recovered {
				if guardGone && onComplete != nil {
					onComplete()
				}
				return recoverErr
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
			return nil
		case *wingwire.LivenessProbe:
			if err := l.cl.write(&wingwire.LivenessAck{Nonce: m.Nonce}); err != nil {
				continue
			}
		}
	}
}

func (l *Lease) recoverWatch() (recovered, guardGone bool, recoverErr error) {
	if l.cl.closed.Load() {
		return false, false, nil
	}
	l.guardMu.Lock()
	defer l.guardMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultReattachTimeout)
	defer cancel()
	if err := l.cl.connect(ctx); err != nil {
		l.cl.opts.logf("lease %s: daemon connection lost and not recovered (%v); run continues without eviction watch or daemon-side cancel", l.RunID, err)
		return false, false, err
	}
	_, terminal, transient := l.cl.readReattach(l.Token)
	if terminal != nil || transient != nil {
		recoverErr := errors.Join(terminal, transient)
		l.cl.opts.logf("lease %s: reattach after daemon restart failed (%v); run continues without eviction watch or daemon-side cancel",
			l.RunID, recoverErr)
		return false, l.guardComplete.Load() && errors.Is(terminal, ErrReattachRejected), recoverErr
	}
	l.guardSent = false
	if l.guardComplete.Load() {
		err := l.sendGuardCompleteLocked()
		return err == nil, false, err
	}
	return true, false, nil
}

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

func (cl *Client) readCancelLease(runID string) (found bool, terminal, transient error) {
	if err := cl.write(&wingwire.CancelLease{RunID: runID}); err != nil {
		return false, nil, err
	}
	msg, err := cl.dec.read()
	if err != nil {
		return false, nil, err
	}
	if refusal := cl.lacksOperation(msg); refusal != nil {
		return false, refusal, nil
	}
	ack, ok := msg.(*wingwire.CancelLeaseAck)
	if !ok {
		return false, fmt.Errorf("wingd/client: expected cancel_lease_ack, got %T", msg), nil
	}
	return ack.Found, nil, nil
}

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
