package cluster

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func parseReserve(s string) (reserve, error) {
	b, err := wingd.ParseBudget(s)
	if err != nil {
		return reserve{}, err
	}
	return reserve{
		cores:          b.Cores,
		coresFraction:  b.CoresFraction,
		memoryBytes:    int64(b.MemoryBytes),
		memoryFraction: b.MemoryFraction,
	}, nil
}

type headroomProvider func(ctx context.Context) *client.Headroom

type reserve struct {
	cores          float64
	coresFraction  float64
	memoryBytes    int64
	memoryFraction float64
}

func (rv reserve) resolve(machineCores float64, machineMemoryBytes int64) (float64, int64) {
	cores := rv.cores
	if rv.coresFraction > 0 {
		cores = rv.coresFraction * machineCores
	}
	mem := rv.memoryBytes
	if rv.memoryFraction > 0 {
		mem = int64(rv.memoryFraction * float64(machineMemoryBytes))
	}
	return cores, mem
}

func advertisedHeadroom(qs wingwire.QueueState, rv reserve) client.Headroom {
	var availCores, machineCores float64
	var availMem, machineMem int64
	for _, r := range qs.Resources {
		switch r.Key {
		case "cores":
			availCores = grantable(r)
			machineCores = r.Capacity
		case "memory":
			availMem = int64(grantable(r))
			machineMem = int64(r.Capacity)
		}
	}
	reserveCores, reserveMem := rv.resolve(machineCores, machineMem)
	cores := availCores - reserveCores
	if cores < 0 {
		cores = 0
	}
	mem := availMem - reserveMem
	if mem < 0 {
		mem = 0
	}
	return client.Headroom{Cores: cores, MemoryBytes: mem, QueueDepth: len(qs.Waiters)}
}

func grantable(r wingwire.ResourceState) float64 {
	if r.Available > 0 || r.Reserved > 0 || r.External > 0 {
		return r.Available
	}
	free := r.Capacity - r.Held
	if free < 0 {
		free = 0
	}
	return free
}

func newHeadroomProvider(home, version string, rv reserve) headroomProvider {
	return func(ctx context.Context) *client.Headroom {
		qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: home, Version: version})
		if err != nil {
			return nil
		}
		h := advertisedHeadroom(qs, rv)
		return &h
	}
}
