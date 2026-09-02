package wingd

import (
	"math"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type etaClaim struct {
	key      string
	capacity uint64
	cost     uint64
	policy   admission.Policy
}

type etaRun struct {
	waiter    int
	admit     uint64
	backfill  uint64
	cores     int64
	softCores bool
	mem       uint64
	claims    []etaClaim
	finish    float64
	duration  float64
}

type etaSimulation struct {
	totalCores int64
	capCores   int64
	capMem     uint64
	lastCap    map[string]uint64
	active     []*etaRun
	waiting    []*etaRun
}

func simulateAdmissionETA(qs *wingwire.QueueState, snap admission.Snapshot) ([]float64, float64) {
	sim := etaSimulation{
		totalCores: snap.TotalMilliCores,
		capCores:   min64(snap.TotalMilliCores, snap.HeadroomMilliCores),
		capMem:     minU64(snap.TotalMemoryBytes, snap.HeadroomMemoryBytes),
		lastCap:    map[string]uint64{},
	}
	for _, sem := range snap.Semaphores {
		sim.lastCap[sem.Key] = nonnegativeInt(sem.LastCapacity)
	}
	rows := semaphoreETAHolderRows(qs, snap)
	for _, lease := range snap.Leases {
		claims := liveETAClaims(snap, lease)
		if lease.MilliCores <= 0 && lease.MemoryBytes == 0 && len(claims) == 0 {
			continue
		}
		row := rows[lease.ID]
		sim.active = append(sim.active, &etaRun{
			admit:  lease.Admit,
			cores:  lease.MilliCores,
			mem:    lease.MemoryBytes,
			claims: claims,
			finish: remainingMS(row.ExpectedDurationMS, row.ElapsedMS),
		})
	}
	for i, waiter := range snap.Waiters {
		sim.waiting = append(sim.waiting, &etaRun{
			waiter:    i,
			admit:     waiter.Admit,
			backfill:  waiter.BackfillCount,
			cores:     waiter.MilliCores,
			softCores: waiter.SoftCores,
			mem:       waiter.MemoryBytes,
			claims:    etaClaims(waiter.Claims),
			duration:  durationMS(qs.Waiters[i].ExpectedDurationMS),
		})
	}

	starts := make([]float64, len(qs.Waiters))
	for i := range starts {
		starts[i] = math.Inf(1)
	}
	now := 0.0
	for len(sim.waiting) > 0 {
		if index := sim.nextPromotable(); index >= 0 {
			run := sim.waiting[index]
			for i := 0; i < index; i++ {
				if !sim.fits(sim.waiting[i]) && etaRunsCompete(sim.waiting[i], run) {
					sim.waiting[i].backfill++
				}
			}
			sim.waiting = append(sim.waiting[:index], sim.waiting[index+1:]...)
			sim.grant(run)
			run.finish = now + run.duration
			sim.active = append(sim.active, run)
			starts[run.waiter] = now
			continue
		}
		next := sim.nextRelease()
		if math.IsInf(next, 1) {
			return starts, math.Inf(1)
		}
		now = next
		sim.releaseAt(next)
	}
	clear := now
	for _, run := range sim.active {
		clear = math.Max(clear, run.finish)
	}
	return starts, clear
}

func etaClaims(claims []admission.ClaimState) []etaClaim {
	out := make([]etaClaim, 0, len(claims))
	for _, claim := range claims {
		out = append(out, etaClaim{key: claim.Key, capacity: nonnegativeInt(claim.Capacity), cost: nonnegativeInt(claim.Cost), policy: claim.Policy})
	}
	return out
}

func liveETAClaims(snap admission.Snapshot, lease admission.LeaseState) []etaClaim {
	byKey := map[string]admission.ClaimState{}
	for _, claim := range lease.Claims {
		byKey[claim.Key] = claim
	}
	var out []etaClaim
	for _, sem := range snap.Semaphores {
		claim, ok := byKey[sem.Key]
		if !ok {
			continue
		}
		for _, hold := range sem.Holds {
			if hold.Lease == lease.ID && !hold.Superseded {
				out = append(out, etaClaim{key: claim.Key, capacity: nonnegativeInt(hold.Capacity), cost: nonnegativeInt(hold.Cost), policy: claim.Policy})
				break
			}
		}
	}
	return out
}

func (s *etaSimulation) nextPromotable() int {
	blocked := map[string]bool{}
	var protected []*etaRun
	for i, run := range s.waiting {
		resources := etaFIFOResources(run)
		if s.violatesReservation(run, protected) || etaAnyBlocked(blocked, resources) {
			etaMarkBlocked(blocked, resources)
			continue
		}
		if s.fits(run) {
			return i
		}
		if run.backfill > 0 {
			protected = append(protected, run)
			continue
		}
		for _, resource := range resources {
			if s.starvedByYounger(run, resource) {
				blocked[resource] = true
			}
		}
	}
	return -1
}

func (s *etaSimulation) fits(run *etaRun) bool {
	usedCores, usedMem := s.hostUsed()
	if !s.resourcesIdle() {
		coresOK := run.cores == 0 || (usedCores <= s.capCores && run.cores <= s.capCores-usedCores)
		if run.softCores {
			coresOK = s.softCoresFit(run.cores, usedCores)
		}
		memoryOK := run.mem == 0 || (usedMem <= s.capMem && run.mem <= s.capMem-usedMem)
		if !coresOK || !memoryOK {
			return false
		}
	}
	for _, claim := range run.claims {
		if claim.policy == admission.PolicyCancelOthers {
			continue
		}
		used, capacity := s.semaphoreBudget(claim.key, claim.capacity)
		if !etaFitsCost(used, claim.cost, capacity) {
			return false
		}
	}
	return true
}

func (s *etaSimulation) hostUsed() (int64, uint64) {
	var cores int64
	var mem uint64
	for _, run := range s.active {
		cores = saturatingAddInt64(cores, run.cores)
		mem = saturatingAddUint64(mem, run.mem)
	}
	return cores, mem
}

func (s *etaSimulation) resourcesIdle() bool {
	for _, run := range s.active {
		if run.cores > 0 || run.mem > 0 {
			return false
		}
		for _, claim := range run.claims {
			if claim.cost > 0 {
				return false
			}
		}
	}
	return true
}

func (s *etaSimulation) softCoresFit(cost, used int64) bool {
	if cost == 0 {
		return true
	}
	if used == 0 {
		return cost <= s.capCores
	}
	if used >= s.totalCores || used > s.capCores {
		return false
	}
	if cost <= s.capCores-used {
		return true
	}
	return used <= s.capCores
}

func (s *etaSimulation) semaphoreBudget(key string, incomingCapacity uint64) (uint64, uint64) {
	var capacity uint64
	var used uint64
	for _, run := range s.active {
		for _, claim := range run.claims {
			if claim.key != key {
				continue
			}
			used = saturatingAddUint64(used, claim.cost)
			if claim.capacity > 0 && (capacity == 0 || claim.capacity < capacity) {
				capacity = claim.capacity
			}
		}
	}
	if incomingCapacity > 0 && (capacity == 0 || incomingCapacity < capacity) {
		capacity = incomingCapacity
	}
	if capacity == 0 {
		capacity = s.lastCap[key]
	}
	if capacity == 0 {
		capacity = 1
	}
	return used, capacity
}

func (s *etaSimulation) grant(run *etaRun) {
	for _, claim := range run.claims {
		if claim.policy != admission.PolicyCancelOthers {
			continue
		}
		for {
			used, capacity := s.semaphoreBudget(claim.key, claim.capacity)
			if etaFitsCost(used, claim.cost, capacity) {
				break
			}
			if !s.dropOldestClaim(claim.key) {
				break
			}
		}
	}
}

func (s *etaSimulation) dropOldestClaim(key string) bool {
	var oldest *etaRun
	claimIndex := -1
	for _, run := range s.active {
		for i, claim := range run.claims {
			if claim.key != key || claim.cost <= 0 {
				continue
			}
			if oldest == nil || run.admit < oldest.admit {
				oldest = run
				claimIndex = i
			}
		}
	}
	if oldest == nil {
		return false
	}
	oldest.claims = append(oldest.claims[:claimIndex], oldest.claims[claimIndex+1:]...)
	return true
}

func (s *etaSimulation) nextRelease() float64 {
	next := math.Inf(1)
	for _, run := range s.active {
		if run.finish < next {
			next = run.finish
		}
	}
	return next
}

func (s *etaSimulation) releaseAt(at float64) {
	kept := s.active[:0]
	for _, run := range s.active {
		if run.finish != at {
			kept = append(kept, run)
		}
	}
	s.active = kept
}

func etaFIFOResources(run *etaRun) []string {
	var out []string
	if run.cores > 0 {
		out = append(out, "cores")
	}
	if run.mem > 0 {
		out = append(out, "memory")
	}
	for _, claim := range run.claims {
		if claim.policy != admission.PolicyCancelOthers {
			out = append(out, "semaphore:"+claim.key)
		}
	}
	return out
}

func etaAnyBlocked(blocked map[string]bool, resources []string) bool {
	for _, resource := range resources {
		if blocked[resource] {
			return true
		}
	}
	return false
}

func etaMarkBlocked(blocked map[string]bool, resources []string) {
	for _, resource := range resources {
		blocked[resource] = true
	}
}

func (s *etaSimulation) starvedByYounger(run *etaRun, resource string) bool {
	demand, used, older, capacity, ok := s.resourceBudget(run, resource)
	return ok && !etaFitsCost(used, demand, capacity) && etaFitsCost(older, demand, capacity)
}

func (s *etaSimulation) resourceBudget(run *etaRun, resource string) (demand, used, older, capacity uint64, ok bool) {
	switch resource {
	case "cores":
		demand, capacity, ok = nonnegativeInt64(run.cores), nonnegativeInt64(s.capCores), true
		for _, holder := range s.active {
			used = saturatingAddUint64(used, nonnegativeInt64(holder.cores))
			if holder.admit <= run.admit {
				older = saturatingAddUint64(older, nonnegativeInt64(holder.cores))
			}
		}
		return demand, used, older, capacity, ok
	case "memory":
		demand, capacity, ok = run.mem, s.capMem, true
		for _, holder := range s.active {
			used = saturatingAddUint64(used, holder.mem)
			if holder.admit <= run.admit {
				older = saturatingAddUint64(older, holder.mem)
			}
		}
		return demand, used, older, capacity, ok
	}
	key := resource[len("semaphore:"):]
	claim, found := etaClaimFor(run, key)
	if !found || claim.policy == admission.PolicyCancelOthers {
		return 0, 0, 0, 0, false
	}
	demand = claim.cost
	used, capacity = s.semaphoreBudget(key, claim.capacity)
	ok = true
	for _, holder := range s.active {
		if holder.admit > run.admit {
			continue
		}
		if held, has := etaClaimFor(holder, key); has {
			older = saturatingAddUint64(older, held.cost)
		}
	}
	return demand, used, older, capacity, ok
}

func etaFitsCost(used, cost, capacity uint64) bool {
	return used <= capacity && cost <= capacity-used
}

func etaClaimFor(run *etaRun, key string) (etaClaim, bool) {
	for _, claim := range run.claims {
		if claim.key == key {
			return claim, true
		}
	}
	return etaClaim{}, false
}

func (s *etaSimulation) violatesReservation(candidate *etaRun, protected []*etaRun) bool {
	for _, waiter := range protected {
		for _, resource := range etaFIFOResources(waiter) {
			candidateCost, ok := etaResourceCost(candidate, resource)
			if !ok || candidateCost == 0 {
				continue
			}
			demand, _, _, capacity, ok := s.resourceBudget(waiter, resource)
			if !ok {
				continue
			}
			var surviving uint64
			for _, holder := range s.active {
				if holder.admit <= waiter.admit || etaRunsConflict(holder, waiter) {
					continue
				}
				cost, _ := etaResourceCost(holder, resource)
				surviving = saturatingAddUint64(surviving, cost)
			}
			if !etaFitsCost(surviving, demand, capacity) || !etaFitsCost(saturatingAddUint64(surviving, demand), candidateCost, capacity) {
				return true
			}
		}
	}
	return false
}

func etaResourceCost(run *etaRun, resource string) (uint64, bool) {
	switch resource {
	case "cores":
		return nonnegativeInt64(run.cores), run.cores > 0
	case "memory":
		return run.mem, run.mem > 0
	default:
		claim, ok := etaClaimFor(run, resource[len("semaphore:"):])
		return claim.cost, ok && claim.policy != admission.PolicyCancelOthers
	}
}

func etaRunsConflict(holder, waiter *etaRun) bool {
	for _, wanted := range waiter.claims {
		if wanted.policy == admission.PolicyCancelOthers || wanted.cost == 0 {
			continue
		}
		for _, held := range holder.claims {
			if held.key == wanted.key && held.policy != admission.PolicyCancelOthers && held.cost > 0 && !etaFitsCost(held.cost, wanted.cost, wanted.capacity) {
				return true
			}
		}
	}
	return false
}

func etaRunsCompete(left, right *etaRun) bool {
	if left.cores > 0 && right.cores > 0 || left.mem > 0 && right.mem > 0 {
		return true
	}
	for _, a := range left.claims {
		if a.policy == admission.PolicyCancelOthers || a.cost == 0 {
			continue
		}
		for _, b := range right.claims {
			if b.policy != admission.PolicyCancelOthers && b.cost > 0 && a.key == b.key {
				return true
			}
		}
	}
	return false
}

func nonnegativeInt(v int) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}

func nonnegativeInt64(v int64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}

func saturatingAddUint64(left, right uint64) uint64 {
	if right > ^uint64(0)-left {
		return ^uint64(0)
	}
	return left + right
}

func saturatingAddInt64(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	if right < 0 && left < math.MinInt64-right {
		return math.MinInt64
	}
	return left + right
}

func finiteETA(v float64) (int64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= float64(math.MaxInt64) {
		return 0, false
	}
	ms := int64(v)
	return ms, ms >= 0
}
