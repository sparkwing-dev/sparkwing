package admission

import (
	"fmt"
	"sort"
)

// Snapshot is the full JSON-serializable ledger state. A snapshot fed to
// [Restore] reproduces the pre-snapshot ledger exactly: the same grants,
// queue order, superseded holders, re-attach tokens, and sequence
// counters, so event sequences and lease IDs continue where they left
// off. Derived quantities (used host budget, queue positions) are not
// stored; Restore recomputes them.
type Snapshot struct {
	TotalMilliCores     int64            `json:"total_milli_cores"`
	TotalMemoryBytes    uint64           `json:"total_memory_bytes"`
	HeadroomMilliCores  int64            `json:"headroom_milli_cores"`
	HeadroomMemoryBytes uint64           `json:"headroom_memory_bytes"`
	LeaseSeq            uint64           `json:"lease_seq"`
	ArrivalSeq          uint64           `json:"arrival_seq"`
	AdmitSeq            uint64           `json:"admit_seq,omitempty"`
	EventSeq            uint64           `json:"event_seq"`
	Leases              []LeaseState     `json:"leases,omitempty"`
	Semaphores          []SemaphoreState `json:"semaphores,omitempty"`
	Waiters             []WaiterState    `json:"waiters,omitempty"`
}

// LeaseState is one live lease in a [Snapshot], in grant order.
type LeaseState struct {
	Seq         uint64       `json:"seq"`
	Admit       uint64       `json:"admit,omitempty"`
	ID          LeaseID      `json:"id"`
	Token       string       `json:"token"`
	RequestID   string       `json:"request_id"`
	MilliCores  int64        `json:"milli_cores"`
	SoftCores   bool         `json:"soft_cores,omitempty"`
	StrictCores bool         `json:"strict_cores,omitempty"`
	MemoryBytes uint64       `json:"memory_bytes"`
	Claims      []ClaimState `json:"claims,omitempty"`
	Members     []string     `json:"members"`
}

// ClaimState is one semaphore claim in a [Snapshot].
type ClaimState struct {
	Key      string `json:"key"`
	Capacity int    `json:"capacity"`
	Cost     int    `json:"cost"`
	Policy   Policy `json:"policy"`
}

// SemaphoreState is one named semaphore in a [Snapshot], holds in grant
// order.
type SemaphoreState struct {
	Key          string      `json:"key"`
	LastCapacity int         `json:"last_capacity"`
	Holds        []HoldState `json:"holds"`
}

// HoldState is one semaphore hold in a [Snapshot]. SupersededBy may name
// a lease that has since been released; it is historical attribution,
// not a live reference.
type HoldState struct {
	Lease        LeaseID `json:"lease"`
	Cost         int     `json:"cost"`
	Capacity     int     `json:"capacity"`
	Superseded   bool    `json:"superseded,omitempty"`
	SupersededBy LeaseID `json:"superseded_by,omitempty"`
}

// WaiterState is one queued request in a [Snapshot], in admission order.
type WaiterState struct {
	Arrival     uint64       `json:"arrival"`
	Admit       uint64       `json:"admit,omitempty"`
	RequestID   string       `json:"request_id"`
	Priority    int          `json:"priority,omitempty"`
	MilliCores  int64        `json:"milli_cores"`
	SoftCores   bool         `json:"soft_cores,omitempty"`
	StrictCores bool         `json:"strict_cores,omitempty"`
	MemoryBytes uint64       `json:"memory_bytes"`
	Claims      []ClaimState `json:"claims,omitempty"`
}

// Snapshot captures the full ledger state deterministically: leases in
// grant order, semaphores sorted by key, waiters in admission order,
// members sorted.
func (l *Ledger) Snapshot() Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()

	snap := Snapshot{
		TotalMilliCores:     l.totalMilliCores,
		TotalMemoryBytes:    l.totalMemory,
		HeadroomMilliCores:  l.headroomMilliCores,
		HeadroomMemoryBytes: l.headroomMemory,
		LeaseSeq:            l.leaseSeq,
		ArrivalSeq:          l.arrivalSeq,
		AdmitSeq:            l.admitSeq,
		EventSeq:            l.eventSeq,
	}
	for _, id := range l.sortedLeaseIDs() {
		le := l.leases[id]
		members := make([]string, 0, len(le.members))
		for m := range le.members {
			members = append(members, m)
		}
		sort.Strings(members)
		snap.Leases = append(snap.Leases, LeaseState{
			Seq:         le.seq,
			Admit:       le.admit,
			ID:          le.id,
			Token:       le.token,
			RequestID:   le.requestID,
			MilliCores:  le.milliCores,
			SoftCores:   le.softCores,
			StrictCores: le.strictCores,
			MemoryBytes: le.memory,
			Claims:      claimStates(le.claims),
			Members:     members,
		})
	}
	keys := make([]string, 0, len(l.sems))
	for key := range l.sems {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sem := l.sems[key]
		state := SemaphoreState{Key: key, LastCapacity: sem.lastCapacity}
		for _, h := range sem.holds {
			state.Holds = append(state.Holds, HoldState{
				Lease:        h.lease,
				Cost:         h.cost,
				Capacity:     h.capacity,
				Superseded:   h.superseded,
				SupersededBy: h.supersededBy,
			})
		}
		snap.Semaphores = append(snap.Semaphores, state)
	}
	for _, w := range l.waiters {
		snap.Waiters = append(snap.Waiters, WaiterState{
			Arrival:     w.arrival,
			Admit:       w.spec.admit,
			RequestID:   w.spec.id,
			Priority:    w.spec.priority,
			MilliCores:  w.spec.milliCores,
			SoftCores:   w.spec.softCores,
			StrictCores: w.spec.strictCores,
			MemoryBytes: w.spec.memory,
			Claims:      claimStates(w.spec.claims),
		})
	}
	return snap
}

func claimStates(claims []claim) []ClaimState {
	var out []ClaimState
	for _, c := range claims {
		out = append(out, ClaimState{Key: c.key, Capacity: c.capacity, Cost: c.cost, Policy: c.policy})
	}
	return out
}

// Restore rebuilds a ledger from a snapshot, validating every field and
// the full invariant set; a snapshot that fails either returns
// [ErrInvalidSnapshot]. tokenGen mints tokens for grants made after the
// restore; nil uses the default random generator.
func Restore(snap Snapshot, tokenGen func() string) (*Ledger, error) {
	if tokenGen == nil {
		tokenGen = randomToken
	}
	if snap.TotalMilliCores < 0 || snap.HeadroomMilliCores < 0 {
		return nil, fmt.Errorf("%w: negative core capacity", ErrInvalidSnapshot)
	}
	l := &Ledger{
		totalMilliCores:    snap.TotalMilliCores,
		totalMemory:        snap.TotalMemoryBytes,
		headroomMilliCores: snap.HeadroomMilliCores,
		headroomMemory:     snap.HeadroomMemoryBytes,
		sems:               map[string]*semaphore{},
		leases:             map[LeaseID]*lease{},
		tokens:             map[string]LeaseID{},
		memberOf:           map[string]LeaseID{},
		leaseSeq:           snap.LeaseSeq,
		arrivalSeq:         snap.ArrivalSeq,
		admitSeq:           snap.AdmitSeq,
		eventSeq:           snap.EventSeq,
		tokenGen:           tokenGen,
	}
	for _, ls := range snap.Leases {
		if err := l.restoreLease(ls); err != nil {
			return nil, err
		}
	}
	for _, ss := range snap.Semaphores {
		if err := l.restoreSemaphore(ss); err != nil {
			return nil, err
		}
	}
	for _, ws := range snap.Waiters {
		if err := l.restoreWaiter(ws); err != nil {
			return nil, err
		}
	}
	if err := l.invariantViolation(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
	}
	return l, nil
}

func (l *Ledger) restoreLease(ls LeaseState) error {
	if ls.ID == "" || ls.Token == "" || ls.RequestID == "" {
		return fmt.Errorf("%w: lease %q with empty id, token, or request id", ErrInvalidSnapshot, ls.ID)
	}
	if _, dup := l.leases[ls.ID]; dup {
		return fmt.Errorf("%w: duplicate lease %s", ErrInvalidSnapshot, ls.ID)
	}
	if _, dup := l.tokens[ls.Token]; dup {
		return fmt.Errorf("%w: duplicate token on lease %s", ErrInvalidSnapshot, ls.ID)
	}
	if ls.MilliCores < 0 {
		return fmt.Errorf("%w: lease %s negative cores", ErrInvalidSnapshot, ls.ID)
	}
	if len(ls.Members) == 0 {
		return fmt.Errorf("%w: lease %s has no members", ErrInvalidSnapshot, ls.ID)
	}
	claims, err := restoreClaims(ls.Claims)
	if err != nil {
		return fmt.Errorf("%w: lease %s: %v", ErrInvalidSnapshot, ls.ID, err)
	}
	le := &lease{
		seq:         ls.Seq,
		admit:       ls.Admit,
		id:          ls.ID,
		token:       ls.Token,
		requestID:   ls.RequestID,
		milliCores:  ls.MilliCores,
		softCores:   ls.SoftCores,
		strictCores: ls.StrictCores,
		memory:      ls.MemoryBytes,
		claims:      claims,
		members:     make(map[string]struct{}, len(ls.Members)),
	}
	for _, m := range ls.Members {
		if m == "" {
			return fmt.Errorf("%w: lease %s has an empty member id", ErrInvalidSnapshot, ls.ID)
		}
		if _, dup := l.memberOf[m]; dup {
			return fmt.Errorf("%w: member %q appears twice", ErrInvalidSnapshot, m)
		}
		le.members[m] = struct{}{}
		l.memberOf[m] = ls.ID
	}
	l.leases[ls.ID] = le
	l.tokens[ls.Token] = ls.ID
	l.usedMilliCores += ls.MilliCores
	l.usedMemory += ls.MemoryBytes
	return nil
}

func (l *Ledger) restoreSemaphore(ss SemaphoreState) error {
	if ss.Key == "" {
		return fmt.Errorf("%w: semaphore with empty key", ErrInvalidSnapshot)
	}
	if _, dup := l.sems[ss.Key]; dup {
		return fmt.Errorf("%w: duplicate semaphore %q", ErrInvalidSnapshot, ss.Key)
	}
	if len(ss.Holds) == 0 {
		return fmt.Errorf("%w: semaphore %q has no holds", ErrInvalidSnapshot, ss.Key)
	}
	sem := &semaphore{lastCapacity: ss.LastCapacity}
	for _, hs := range ss.Holds {
		sem.holds = append(sem.holds, &hold{
			lease:        hs.Lease,
			cost:         hs.Cost,
			capacity:     hs.Capacity,
			superseded:   hs.Superseded,
			supersededBy: hs.SupersededBy,
		})
	}
	l.sems[ss.Key] = sem
	return nil
}

func (l *Ledger) restoreWaiter(ws WaiterState) error {
	if ws.RequestID == "" {
		return fmt.Errorf("%w: waiter with empty request id", ErrInvalidSnapshot)
	}
	if ws.MilliCores < 0 || ws.MilliCores > l.totalMilliCores {
		return fmt.Errorf("%w: waiter %q cores %d outside total %d", ErrInvalidSnapshot, ws.RequestID, ws.MilliCores, l.totalMilliCores)
	}
	if ws.MemoryBytes > l.totalMemory {
		return fmt.Errorf("%w: waiter %q memory %d outside total %d", ErrInvalidSnapshot, ws.RequestID, ws.MemoryBytes, l.totalMemory)
	}
	claims, err := restoreClaims(ws.Claims)
	if err != nil {
		return fmt.Errorf("%w: waiter %q: %v", ErrInvalidSnapshot, ws.RequestID, err)
	}
	l.waiters = append(l.waiters, &waiter{
		arrival: ws.Arrival,
		spec: spec{
			id:          ws.RequestID,
			admit:       ws.Admit,
			priority:    ws.Priority,
			milliCores:  ws.MilliCores,
			softCores:   ws.SoftCores,
			strictCores: ws.StrictCores,
			memory:      ws.MemoryBytes,
			claims:      claims,
		},
	})
	return nil
}

func restoreClaims(states []ClaimState) ([]claim, error) {
	var claims []claim
	seen := make(map[string]bool, len(states))
	for _, cs := range states {
		c, err := normalizeClaim(cs.Key, cs.Capacity, cs.Cost, cs.Policy)
		if err != nil {
			return nil, err
		}
		if seen[c.key] {
			return nil, fmt.Errorf("duplicate semaphore key %q", c.key)
		}
		seen[c.key] = true
		claims = append(claims, c)
	}
	return claims, nil
}
