package client

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
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

// JobFits reports whether the ledger would grant a native job charging cores
// right now, judged from the daemon's own queue state. Zero cores is charged
// the daemon's default, and a charge above the machine's capacity is judged at
// that capacity. An idle machine always fits, as it does in the ledger, so an
// estimate stricter than the ledger can delay a job until the holders finish
// but never strand it; admission stays authoritative either way.
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
	for _, waiter := range state.Waiters {
		// safety: the ledger admits host resources first in, first out, so an earlier waiter goes first.
		if slices.Contains(waiter.WaitingOn, "cores") {
			return false
		}
	}
	for _, resource := range state.Resources {
		// safety: Reserved is a share of the whole machine while a budget caps Capacity, so the daemon's
		// idle clamp is never above Capacity and judging at Capacity is never more lenient than the ledger.
		if resource.Key == "cores" && resource.Capacity > 0 && resource.Available < min(cores, resource.Capacity) {
			return false
		}
	}
	return true
}
