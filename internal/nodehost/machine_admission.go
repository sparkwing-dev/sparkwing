package nodehost

import (
	"context"
	"errors"
	"fmt"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// safety: wrapping ErrNoDaemon keeps a previous daemon's log tail out of the error; that log describes
// an earlier incarnation, not why this host found none.
var errNoElectedController = fmt.Errorf("local host requires its elected admission controller: %w", wingdclient.ErrNoDaemon)

// MachineAdmission binds host capacity to the elected local daemon.
func MachineAdmission(home, version string) *LocalAdmission {
	return &LocalAdmission{Daemon: machineAdmitter{options: wingdclient.Options{
		Home: home, Version: version, NoTakeover: true,
		Spawn: func(string, string) error { return errNoElectedController },
	}}}
}

// RunnerMachineAdmission shares the local daemon with an enrolled sparkwing-runner. The runner never
// hosts that daemon, so a missing or mismatched one fails with the runner's own remedies.
func RunnerMachineAdmission(home, version string) *LocalAdmission {
	admission := MachineAdmission(home, version)
	admitter := admission.Daemon.(machineAdmitter)
	admitter.runner = true
	admission.Daemon = admitter
	return admission
}

// RunnerAdmissionUnavailable explains a daemon a runner with runner.local_admission cannot use, or
// returns nil when err is not one.
func RunnerAdmissionUnavailable(version string, err error) error {
	const shared = "sparkwing-runner %s shares this machine's admission (runner.local_admission: true), but "
	switch {
	case errors.Is(err, wingdclient.ErrNoDaemon):
		return fmt.Errorf(shared+"no local controller is running. Start it with `sparkwing dashboard open`, keep it running with `sparkwing daemon install`, or set runner.local_admission: false and restart the runner: %w", version, err)
	case wingdclient.IncompatibleDaemon(err):
		return fmt.Errorf(shared+"the local controller runs a sparkwing build this runner cannot speak to. Install the same sparkwing and sparkwing-runner build (restarting the runner service updates it to the latest release), or set runner.local_admission: false and restart the runner: %w", version, err)
	}
	return nil
}

type machineAdmitter struct {
	options wingdclient.Options
	runner  bool
}

func (a machineAdmitter) Acquire(ctx context.Context, request wingwire.AdmissionRequest, queued func(wingwire.Queued)) (Lease, error) {
	client, err := wingdclient.EnsureDaemon(ctx, a.options)
	if err != nil {
		if explained := RunnerAdmissionUnavailable(a.options.Version, err); a.runner && explained != nil {
			return nil, explained
		}
		return nil, err
	}
	held, err := client.Acquire(ctx, request, queued)
	if held == nil {
		return nil, errors.Join(err, client.Close())
	}
	return machineLease{held}, err
}

type machineLease struct{ lease *wingdclient.Lease }

func (l machineLease) Token() string  { return l.lease.Token }
func (l machineLease) Release() error { return l.lease.Release() }
func (l machineLease) TrackProcess(ctx context.Context, pid int) error {
	return l.lease.TrackProcess(ctx, pid)
}

func (l machineLease) TrackCommand(ctx context.Context, pid int, birth string) error {
	return l.lease.TrackCommand(ctx, pid, birth)
}

func (l machineLease) Watch(evicted func(wingwire.Evicted), stopped func(wingwire.Cancel)) error {
	return l.lease.WatchControl(evicted, stopped)
}

func (a *LocalAdmission) machineHasJobCapacity(ctx context.Context) bool {
	if a == nil {
		return true
	}
	machine, ok := a.Daemon.(machineAdmitter)
	if !ok {
		return true
	}
	state, err := wingdclient.Query(ctx, machine.options)
	// safety: admission remains authoritative when the ledger cannot be observed.
	return err != nil || wingdclient.HasJobCapacity(state)
}
