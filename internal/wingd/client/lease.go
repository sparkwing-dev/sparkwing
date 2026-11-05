package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type Lease struct {
	cl               *Client
	RunID            string
	Token            string
	Resources        wingwire.HostResources
	Semaphores       []string
	SoleRunUnderLoad bool
	ExternalCores    float64
	id               string
	revision         uint64
	ctx              context.Context
	cancel           context.CancelFunc
	once             sync.Once
	releaseErr       error
}

func (cl *Client) Acquire(ctx context.Context, req wingwire.AdmissionRequest, onQueued func(wingwire.Queued)) (held *Lease, resultErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(cl.clientContext(ctx), cancel)
	defer stop()
	pid := req.PID
	if pid == 0 {
		pid = os.Getpid()
	}
	birth, err := procgroup.ProcessBirth(pid)
	if err != nil {
		return nil, fmt.Errorf("wingd/client: holder process: %w", err)
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, err
	}
	request := wingd.AdmissionHTTPRequest{RequestKey: hex.EncodeToString(key[:]), Request: req, RootPID: pid, ProcessBirth: birth}
	granted := false
	defer func() {
		if granted || ctx.Err() == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, cl.admissionCall(cleanup, http.MethodDelete, "/requests/by-key/"+request.RequestKey, nil, nil, readOnlyRetryLimit))
	}()
	var state wingd.AdmissionHTTPState
	if err := cl.admissionCall(ctx, http.MethodPost, "/requests", request, &state, 0); err != nil {
		return nil, err
	}
	id := state.ID
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if state.Grant != nil {
			granted = true
			return cl.newLease(ctx, state), nil
		}
		if state.Evicted != nil {
			return nil, cl.admissionError(state.Evicted)
		}
		if state.Cancel != nil {
			return nil, &cancelledError{Reason: state.Cancel.Reason}
		}
		if state.State == "released" {
			return nil, ErrReattachRejected
		}
		if id == "" {
			return nil, errors.New("wingd/client: admission returned no request ticket")
		}
		if state.Queued != nil && onQueued != nil {
			onQueued(*state.Queued)
		}
		revision := state.Revision
		state = wingd.AdmissionHTTPState{}
		if err := cl.admissionCall(ctx, http.MethodGet, "/requests/"+url.PathEscape(id)+"?wait=true&after="+strconv.FormatUint(revision, 10), nil, &state, 0); err != nil {
			return nil, err
		}
	}
}

func (cl *Client) newLease(ctx context.Context, state wingd.AdmissionHTTPState) *Lease {
	grant := state.Grant
	ctx, cancel := context.WithCancel(cl.clientContext(ctx))
	return &Lease{
		cl: cl, id: state.ID, revision: state.Revision, ctx: ctx, cancel: cancel, RunID: grant.RunID, Token: grant.LeaseToken,
		Resources: grant.Resources, Semaphores: grant.Semaphores, SoleRunUnderLoad: grant.SoleRunUnderLoad, ExternalCores: grant.ExternalCores,
	}
}

// Reattach observes a durable lease after a controller restart.
func (cl *Client) Reattach(ctx context.Context, token, runID string) (*Lease, error) {
	var state wingd.AdmissionHTTPState
	if err := cl.admissionCall(ctx, http.MethodGet, "/leases/"+url.PathEscape(token), nil, &state, 0); err != nil {
		if admissionNotFound(err) {
			return nil, errors.Join(ErrReattachRejected, err)
		}
		return nil, err
	}
	if state.Grant == nil || (runID != "" && state.Grant.RunID != runID) {
		return nil, ErrReattachRejected
	}
	return cl.newLease(ctx, state), nil
}

func (l *Lease) Release() error {
	l.once.Do(func() {
		l.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
		defer cancel()
		l.releaseErr = l.cl.admissionCall(ctx, http.MethodDelete, "/requests/"+url.PathEscape(l.id), nil, nil, 0)
		l.releaseErr = errors.Join(l.releaseErr, l.cl.Close())
	})
	return l.releaseErr
}

// Watch observes durable lease cancellation until Release.
func (l *Lease) Watch(onEvicted func(wingwire.Evicted)) {
	if err := l.WatchControl(onEvicted, nil); err != nil {
		l.cl.opts.logf("lease %s: watch ended: %v", l.RunID, err)
	}
}

// WatchControl joins request-based lease watching and reports lost authority.
func (l *Lease) WatchControl(onEvicted func(wingwire.Evicted), onCancel func(wingwire.Cancel)) error {
	revision := l.revision
	for l.ctx.Err() == nil {
		var state wingd.AdmissionHTTPState
		err := l.cl.admissionCall(l.ctx, http.MethodGet, "/requests/"+url.PathEscape(l.id)+"/watch?wait=true&after="+strconv.FormatUint(revision, 10), nil, &state, 0)
		if l.ctx.Err() != nil {
			break
		}
		if err != nil {
			if admissionNotFound(err) {
				return errors.Join(ErrReattachRejected, err)
			}
			return err
		}
		revision = state.Revision
		if state.Evicted != nil {
			if onEvicted != nil {
				onEvicted(*state.Evicted)
			}
			return nil
		}
		if state.Cancel != nil {
			if onCancel != nil {
				onCancel(*state.Cancel)
			}
			return nil
		}
		if state.State == "released" {
			return ErrReattachRejected
		}
	}
	return nil //nolint:nilerr // safety: Release cancels observation after relinquishing authority.
}

func (cl *Client) CancelLease(ctx context.Context, runID string) (bool, error) {
	var result wingwire.CancelLeaseAck
	err := cl.admissionCall(ctx, http.MethodPost, "/cancel", wingwire.CancelLease{RunID: runID}, &result, readOnlyRetryLimit)
	return result.Found, err
}
