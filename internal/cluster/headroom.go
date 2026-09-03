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

type capacityReport struct {
	headroom *client.Headroom
	budget   resourceBudget
}

type resourceBudget struct {
	Cores       float64
	MemoryBytes int64
}

type headroomProvider func(ctx context.Context) capacityReport

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

func advertisedHeadroom(qs wingwire.QueueState, rv reserve) *client.Headroom {
	return advertisedCapacity(qs, rv, reserve{}, reserve{}).headroom
}

func advertisedCapacity(qs wingwire.QueueState, rv, contribution, membershipContribution reserve) capacityReport {
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
	budgetCores, budgetMem := contribution.resolve(machineCores, machineMem)
	if budgetCores <= 0 {
		budgetCores = machineCores - reserveCores
	}
	if budgetMem <= 0 {
		budgetMem = machineMem - reserveMem
	}
	membershipCores, membershipMem := membershipContribution.resolve(machineCores, machineMem)
	if membershipCores > 0 {
		budgetCores = min(budgetCores, membershipCores)
	}
	if membershipMem > 0 {
		budgetMem = min(budgetMem, membershipMem)
	}
	budgetCores = min(budgetCores, machineCores-reserveCores)
	budgetMem = min(budgetMem, machineMem-reserveMem)
	budgetCores = max(budgetCores, 0)
	budgetMem = max(budgetMem, 0)
	var controllerCores float64
	var controllerMem int64
	for _, holder := range qs.Holders {
		if holder.Origin == wingwire.OriginController {
			controllerCores += holder.Resources.Cores
			controllerMem += holder.Resources.MemoryBytes
		}
	}
	cores := availCores - reserveCores
	cores = min(cores, budgetCores-controllerCores)
	if cores < 0 {
		cores = 0
	}
	mem := availMem - reserveMem
	mem = min(mem, budgetMem-controllerMem)
	if mem < 0 {
		mem = 0
	}
	return capacityReport{
		headroom: &client.Headroom{Cores: cores, MemoryBytes: mem, QueueDepth: len(qs.Waiters)},
		budget:   resourceBudget{Cores: budgetCores, MemoryBytes: budgetMem},
	}
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

func newHeadroomProvider(home, version string, rv, contribution, membershipContribution reserve) headroomProvider {
	return func(ctx context.Context) capacityReport {
		qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: home, Version: version})
		if err != nil {
			return capacityReport{}
		}
		return advertisedCapacity(qs, rv, contribution, membershipContribution)
	}
}
