package admission

import (
	"fmt"
	"sort"
)

// mustHoldInvariants re-asserts every ledger invariant and panics on the
// first violation. Every mutating method calls it before returning, so a
// transition that corrupts the ledger fails at its own boundary instead
// of committing and surfacing later as an unrelated symptom.
func (l *Ledger) mustHoldInvariants() {
	if err := l.invariantViolation(); err != nil {
		panic(fmt.Sprintf("admission: ledger invariant violated: %v", err))
	}
}

// invariantViolation checks the full invariant set and returns the first
// violation found, or nil. The checks: host accounting matches the lease
// set, memory never exceeds the total, every lease has at least one member,
// consistent membership indexing, and exactly one hold per claim; every
// semaphore's live cost fits its effective capacity and every hold belongs
// to a live lease; waiters are unique, strictly FIFO-ordered, and hold
// nothing; no waiter is left behind free capacity that fits it; and no
// sequence counter has fallen behind the state it numbers.
func (l *Ledger) invariantViolation() error {
	if err := l.hostInvariant(); err != nil {
		return err
	}
	if err := l.leaseInvariants(); err != nil {
		return err
	}
	if err := l.semaphoreInvariants(); err != nil {
		return err
	}
	if err := l.waiterInvariants(); err != nil {
		return err
	}
	if i := l.nextPromotable(); i >= 0 {
		return fmt.Errorf("waiter %q fits free capacity but is still queued", l.waiters[i].spec.id)
	}
	return nil
}

func (l *Ledger) hostInvariant() error {
	var cores int64
	var memory uint64
	for _, le := range l.leases {
		cores += le.milliCores
		memory += le.memory
	}
	if cores != l.usedMilliCores {
		return fmt.Errorf("used cores %d does not match lease sum %d", l.usedMilliCores, cores)
	}
	if memory != l.usedMemory {
		return fmt.Errorf("used memory %d does not match lease sum %d", l.usedMemory, memory)
	}
	if l.usedMilliCores > l.totalMilliCores && !l.softCoreOvercommit() {
		return fmt.Errorf("granted hard cores %d exceed total %d", l.usedMilliCores, l.totalMilliCores)
	}
	if l.usedMemory > l.totalMemory {
		return fmt.Errorf("granted memory %d exceeds total %d", l.usedMemory, l.totalMemory)
	}
	if l.usedMilliCores < 0 {
		return fmt.Errorf("used cores %d negative", l.usedMilliCores)
	}
	if l.headroomMilliCores < 0 {
		return fmt.Errorf("headroom cores %d negative", l.headroomMilliCores)
	}
	return nil
}

func (l *Ledger) softCoreOvercommit() bool {
	for _, le := range l.leases {
		if le.softCores {
			return true
		}
	}
	return false
}

func (l *Ledger) leaseInvariants() error {
	members := 0
	seenSeq := make(map[uint64]bool, len(l.leases))
	for _, id := range l.sortedLeaseIDs() {
		le := l.leases[id]
		if le.id != id {
			return fmt.Errorf("lease %s indexed under %s", le.id, id)
		}
		if len(le.members) == 0 {
			return fmt.Errorf("lease %s is alive with no members", id)
		}
		if le.seq == 0 || le.seq > l.leaseSeq {
			return fmt.Errorf("lease %s seq %d outside counter %d", id, le.seq, l.leaseSeq)
		}
		if seenSeq[le.seq] {
			return fmt.Errorf("lease %s reuses seq %d", id, le.seq)
		}
		seenSeq[le.seq] = true
		if l.tokens[le.token] != id {
			return fmt.Errorf("lease %s token not indexed", id)
		}
		for m := range le.members {
			if l.memberOf[m] != id {
				return fmt.Errorf("member %q of lease %s not indexed", m, id)
			}
		}
		members += len(le.members)
		for _, c := range le.claims {
			if err := l.holdMatchesClaim(id, c); err != nil {
				return err
			}
		}
	}
	if members != len(l.memberOf) {
		return fmt.Errorf("member index has %d entries, leases have %d members", len(l.memberOf), members)
	}
	if len(l.tokens) != len(l.leases) {
		return fmt.Errorf("token index has %d entries for %d leases", len(l.tokens), len(l.leases))
	}
	return nil
}

func (l *Ledger) holdMatchesClaim(id LeaseID, c claim) error {
	sem := l.sems[c.key]
	if sem == nil {
		return fmt.Errorf("lease %s claims semaphore %q which has no state", id, c.key)
	}
	matches := 0
	for _, h := range sem.holds {
		if h.lease != id {
			continue
		}
		matches++
		if h.cost != c.cost || h.capacity != c.capacity {
			return fmt.Errorf("lease %s hold on %q (%d/%d) disagrees with its claim (%d/%d)",
				id, c.key, h.cost, h.capacity, c.cost, c.capacity)
		}
	}
	if matches != 1 {
		return fmt.Errorf("lease %s has %d holds on semaphore %q, want 1", id, matches, c.key)
	}
	return nil
}

func (l *Ledger) semaphoreInvariants() error {
	keys := make([]string, 0, len(l.sems))
	for key := range l.sems {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sem := l.sems[key]
		if len(sem.holds) == 0 {
			return fmt.Errorf("semaphore %q tracked with no holds", key)
		}
		if sem.lastCapacity < 1 {
			return fmt.Errorf("semaphore %q last capacity %d", key, sem.lastCapacity)
		}
		for _, h := range sem.holds {
			le, ok := l.leases[h.lease]
			if !ok {
				return fmt.Errorf("semaphore %q hold references dead lease %s", key, h.lease)
			}
			if !leaseClaims(le, key) {
				return fmt.Errorf("semaphore %q hold by lease %s has no matching claim", key, h.lease)
			}
			if h.cost < 0 || h.capacity < 1 || h.cost > h.capacity {
				return fmt.Errorf("semaphore %q hold by lease %s has cost %d capacity %d", key, h.lease, h.cost, h.capacity)
			}
			if h.superseded && h.supersededBy == "" {
				return fmt.Errorf("semaphore %q superseded hold by lease %s names no superseder", key, h.lease)
			}
			if !h.superseded && h.supersededBy != "" {
				return fmt.Errorf("semaphore %q live hold by lease %s names superseder %s", key, h.lease, h.supersededBy)
			}
		}
		used := l.semUsed(key)
		if eff := l.semEffectiveCapacity(key, 0); used > eff {
			return fmt.Errorf("semaphore %q live cost %d exceeds effective capacity %d", key, used, eff)
		}
	}
	return nil
}

func leaseClaims(le *lease, key string) bool {
	for _, c := range le.claims {
		if c.key == key {
			return true
		}
	}
	return false
}

func (l *Ledger) waiterInvariants() error {
	seen := make(map[string]bool, len(l.waiters))
	for i, w := range l.waiters {
		if i > 0 && !waiterLess(l.waiters[i-1], w) {
			return fmt.Errorf("waiter %q is out of priority order", w.spec.id)
		}
		if w.arrival > l.arrivalSeq {
			return fmt.Errorf("waiter %q arrival %d outside counter %d", w.spec.id, w.arrival, l.arrivalSeq)
		}
		if seen[w.spec.id] {
			return fmt.Errorf("participant %q waits twice", w.spec.id)
		}
		seen[w.spec.id] = true
		if _, holds := l.memberOf[w.spec.id]; holds {
			return fmt.Errorf("participant %q both holds and waits", w.spec.id)
		}
	}
	return nil
}

func waiterLess(a, b *waiter) bool {
	if a.spec.priority != b.spec.priority {
		return a.spec.priority > b.spec.priority
	}
	return a.arrival < b.arrival
}

func (l *Ledger) sortedLeaseIDs() []LeaseID {
	byID := make([]*lease, 0, len(l.leases))
	for _, le := range l.leases {
		byID = append(byID, le)
	}
	sort.Slice(byID, func(i, j int) bool { return byID[i].seq < byID[j].seq })
	ids := make([]LeaseID, len(byID))
	for i, le := range byID {
		ids[i] = le.id
	}
	return ids
}
