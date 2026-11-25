package client

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

var ErrNoDaemon = errors.New("wingd/client: no daemon running")

var errDaemonUnreachable = errors.New("wingd/client: could not reach the admission daemon")

// errDaemonWedged is a daemon that took the connection and then answered
// nothing before the caller's budget ran out. It is distinct from unreachable
// because the socket is held: no successor can bind it, no admission is being
// arbitrated behind it, and a drain request cannot reach it.
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

// JobFits is an advisory estimate of whether the ledger could grant a native job
// charging cores right now, judged from the daemon's queue state. It reports
// false only when the charge certainly cannot fit, and true whenever it cannot
// tell, because admission stays authoritative and a wrong "fits" costs one
// refused claim while a wrong "full" would hold the job back. Zero cores is
// charged the daemon's default, and an idle machine always fits, as in the
// ledger.
func JobFits(state wingwire.QueueState, cores float64) bool {
	if state.AdmissionMode == string(admission.ModeOff) {
		return true
	}
	if cores <= 0 {
		cores = admission.DefaultChargeCores
	}
	idle := true
	for _, resource := range state.Resources {
		idle = idle && resource.Held <= 0
	}
	if idle {
		return true
	}
	for _, resource := range state.Resources {
		// safety: the daemon clamps a charge to its idle grantable cores, which are never below the ledger's current
		// room (Available plus Held), so that room bounds the charge from below without the daemon's private state.
		if resource.Key == "cores" && resource.Capacity > 0 && resource.Available < min(cores, resource.Available+resource.Held) {
			return false
		}
	}
	return true
}
