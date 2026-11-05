package client

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

var ErrNoDaemon = errors.New("wingd/client: no daemon running")

var errDaemonUnreachable = errors.New("wingd/client: could not reach the admission daemon")

// errDaemonWedged is a daemon that took the connection and then answered
// nothing before the caller's budget ran out. It is distinct from unreachable
// because the socket is held: no successor can bind it, no admission is being
// arbitrated behind it, and a drain that needs a handshake cannot replace it.
var errDaemonWedged = errors.New("wingd/client: the daemon accepted the connection and did not answer")

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
	return fmt.Errorf("%w at %s: %w", errDaemonUnreachable, sock, dialErr)
}

func (cl *Client) QueueState(ctx context.Context) (wingwire.QueueState, error) {
	var state wingwire.QueueState
	err := cl.admissionCall(ctx, http.MethodGet, "/queue", nil, &state, readOnlyRetryLimit)
	return state, err
}

func Query(ctx context.Context, opts Options) (wingwire.QueueState, error) {
	noSpawn := queryOptions(opts)
	cl, err := EnsureDaemon(ctx, noSpawn)
	if err != nil {
		if errors.Is(err, errDaemonUnreachable) {
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

// HasJobCapacity reports whether the ledger can admit another native job.
func HasJobCapacity(state wingwire.QueueState) bool {
	for _, resource := range state.Resources {
		if (resource.Key == "cores" || resource.Key == "memory") && resource.Capacity > 0 && resource.Available <= 0 {
			return false
		}
	}
	return true
}
