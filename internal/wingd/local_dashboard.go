package wingd

import (
	"context"
	"errors"
)

type localDashboardRequest struct {
	ctx     context.Context
	address string
	result  chan error
}

func (r *localRuntime) EnsureDashboard(ctx context.Context, address string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if address == "" {
		return errors.New("wingd: dashboard address is required")
	}
	r.mu.Lock()
	session := r.run
	ready := session != nil && r.statusLocked(session).Running
	r.mu.Unlock()
	if !ready {
		return errors.New("wingd: local controller is not ready")
	}
	request := localDashboardRequest{ctx: ctx, address: address, result: make(chan error, 1)}
	select {
	case session.dashboard <- request:
	case <-session.done:
		return session.stoppedError()
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.result:
		return err
	case <-session.done:
		return session.stoppedError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (session *localRuntimeSession) stoppedError() error {
	if session.err != nil {
		return session.err
	}
	return errors.New("wingd: local controller stopped")
}
