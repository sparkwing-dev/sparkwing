package nodehost

import (
	"context"
	"errors"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// MachineAdmission binds host capacity to the elected local daemon.
func MachineAdmission(home, version string) *LocalAdmission {
	return &LocalAdmission{Daemon: machineAdmitter{options: wingdclient.Options{
		Home: home, Version: version, NoTakeover: true,
		Spawn: func(string, string) error { return errors.New("local host requires its elected admission controller") },
	}}}
}

type machineAdmitter struct{ options wingdclient.Options }

func (a machineAdmitter) Acquire(ctx context.Context, request wingwire.AdmissionRequest, queued func(wingwire.Queued)) (Lease, error) {
	client, err := wingdclient.EnsureDaemon(ctx, a.options)
	if err != nil {
		return nil, err
	}
	held, err := client.Acquire(ctx, request, queued)
	if held == nil {
		return nil, errors.Join(err, client.Close())
	}
	return machineLease{held}, err
}

type machineLease struct{ lease *wingdclient.Lease }

func (l machineLease) Resources() wingwire.HostResources { return l.lease.Resources }

func (l machineLease) Token() string  { return l.lease.Token }
func (l machineLease) Release() error { return l.lease.Release() }
func (l machineLease) Watch(evicted func(wingwire.Evicted), stopped func(wingwire.Cancel)) error {
	return l.lease.WatchControl(evicted, stopped)
}
