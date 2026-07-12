package cluster

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// parseReserve reads a local-reserve setting in the same grammar as the
// daemon budget (e.g. "2", "2,4gb", "10%", "10%,20%"), so the reserve knob
// mirrors the budget surface an operator already knows. An empty string is
// a zero reserve with no error.
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

// headroomProvider yields the box's live free capacity to advertise to the
// controller, or nil when nothing should be advertised (no local daemon
// engaged, or the daemon is unreachable). It is called before every claim
// and heartbeat so the controller always sees a fresh figure.
type headroomProvider func(ctx context.Context) *client.Headroom

// reserve is a fixed amount of host capacity a runner holds back from what
// it advertises to the controller, so the operator keeps room for local
// work the controller must not fill. Fractions resolve against the
// machine totals the daemon reports.
type reserve struct {
	cores          float64
	coresFraction  float64
	memoryBytes    int64
	memoryFraction float64
}

// resolve returns the reserve as absolute cores and memory against a
// machine of the given size, so a fraction reserve tracks the box it runs
// on. A dimension left unset reserves nothing.
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

// advertisedHeadroom computes what a runner tells the controller is free:
// the daemon's grantable cores and memory minus the local reserve, floored
// at zero, plus the daemon's queue depth. It is pure so the reserve
// arithmetic is testable without a live daemon.
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

// grantable is the amount a host resource row reports as grantable right
// now: the daemon's headroom-aware Available when it sent one, else plain
// capacity-minus-held for an older daemon.
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

// newHeadroomProvider returns a provider that queries the local admission
// daemon at home and advertises its grantable capacity minus rv. It
// returns nil (advertise nothing) when no daemon is reachable, so a runner
// whose box has no local daemon simply claims without a headroom figure.
func newHeadroomProvider(home string, rv reserve) headroomProvider {
	return func(ctx context.Context) *client.Headroom {
		qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: home})
		if err != nil {
			return nil
		}
		h := advertisedHeadroom(qs, rv)
		return &h
	}
}
