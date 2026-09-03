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
	_, membershipBudget := resolvedContributionBudgets(machineCores, machineMem, rv, contribution, membershipContribution)
	var controllerCores float64
	var controllerMem int64
	for _, holder := range qs.Holders {
		if holder.Origin == wingwire.OriginController {
			controllerCores += holder.Resources.Cores
			controllerMem += holder.Resources.MemoryBytes
		}
	}
	cores := availCores - reserveCores
	cores = min(cores, membershipBudget.Cores-controllerCores)
	if cores < 0 {
		cores = 0
	}
	mem := availMem - reserveMem
	mem = min(mem, membershipBudget.MemoryBytes-controllerMem)
	if mem < 0 {
		mem = 0
	}
	return capacityReport{
		headroom: &client.Headroom{Cores: cores, MemoryBytes: mem, QueueDepth: len(qs.Waiters)},
		budget:   membershipBudget,
	}
}

func resolvedContributionBudgets(machineCores float64, machineMemoryBytes int64, localReserve, globalContribution, membershipContribution reserve) (resourceBudget, resourceBudget) {
	reserveCores, reserveMemory := localReserve.resolve(machineCores, machineMemoryBytes)
	globalCores, globalMemory := globalContribution.resolve(machineCores, machineMemoryBytes)
	if globalCores <= 0 {
		globalCores = machineCores - reserveCores
	}
	if globalMemory <= 0 {
		globalMemory = machineMemoryBytes - reserveMemory
	}
	global := resourceBudget{
		Cores:       max(min(globalCores, machineCores-reserveCores), 0),
		MemoryBytes: max(min(globalMemory, machineMemoryBytes-reserveMemory), 0),
	}
	membershipCores, membershipMemory := membershipContribution.resolve(machineCores, machineMemoryBytes)
	if membershipCores <= 0 {
		membershipCores = global.Cores
	}
	if membershipMemory <= 0 {
		membershipMemory = global.MemoryBytes
	}
	membership := resourceBudget{
		Cores:       max(min(membershipCores, global.Cores), 0),
		MemoryBytes: max(min(membershipMemory, global.MemoryBytes), 0),
	}
	return global, membership
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
