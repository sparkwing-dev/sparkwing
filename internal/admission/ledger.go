package admission

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"sync"
)

// Config sets a new ledger's fixed host capacity and optional token
// generation. Totals are the machine's full budget; the dynamic
// load-sensed value is fed later through [Ledger.SetHeadroom].
type Config struct {
	// TotalCores is the host CPU budget in cores; fractional values are
	// allowed. Must be finite and non-negative.
	TotalCores float64
	// TotalMemoryBytes is the host memory budget.
	TotalMemoryBytes uint64
	// TokenGen mints re-attach tokens for new leases. Nil uses a
	// cryptographically random generator. Generated tokens must be
	// non-empty; duplicates are retried.
	TokenGen func() string
}

// Ledger is the unified admission ledger. It is pure state plus
// transitions: no clocks, no goroutines, no I/O. All methods are safe
// for concurrent use; mutations serialize on an internal lock and each
// returns the events it produced. Every transition re-asserts the ledger
// invariants and panics on violation -- an invariant break is a bug the
// caller must treat as fatal, never a condition to log and continue.
type Ledger struct {
	mu                 sync.Mutex
	totalMilliCores    int64
	totalMemory        uint64
	headroomMilliCores int64
	headroomMemory     uint64
	usedMilliCores     int64
	usedMemory         uint64
	sems               map[string]*semaphore
	leases             map[LeaseID]*lease
	tokens             map[string]LeaseID
	memberOf           map[string]LeaseID
	waiters            []*waiter
	leaseSeq           uint64
	arrivalSeq         uint64
	admitSeq           uint64
	eventSeq           uint64
	tokenGen           func() string
}

type spec struct {
	id          string
	admit       uint64
	milliCores  int64
	softCores   bool
	strictCores bool
	memory      uint64
	claims      []claim
}

type claim struct {
	key      string
	capacity int
	cost     int
	policy   Policy
}

type lease struct {
	seq         uint64
	admit       uint64
	id          LeaseID
	token       string
	requestID   string
	milliCores  int64
	softCores   bool
	strictCores bool
	memory      uint64
	claims      []claim
	members     map[string]struct{}
}

type semaphore struct {
	lastCapacity int
	holds        []*hold
}

type hold struct {
	lease        LeaseID
	cost         int
	capacity     int
	superseded   bool
	supersededBy LeaseID
}

type waiter struct {
	arrival uint64
	spec    spec
}

type resource string

const (
	resourceCores  resource = "cores"
	resourceMemory resource = "memory"
)

func semResource(key string) resource { return resource("semaphore:" + key) }

const maxMilliCores = int64(1) << 50

// New constructs an empty ledger with the given host capacity. Headroom
// starts equal to the totals.
func New(cfg Config) (*Ledger, error) {
	mc, err := toMilliCores(cfg.TotalCores)
	if err != nil {
		return nil, fmt.Errorf("%w: total cores %v", ErrInvalidConfig, cfg.TotalCores)
	}
	gen := cfg.TokenGen
	if gen == nil {
		gen = randomToken
	}
	return &Ledger{
		totalMilliCores:    mc,
		totalMemory:        cfg.TotalMemoryBytes,
		headroomMilliCores: mc,
		headroomMemory:     cfg.TotalMemoryBytes,
		sems:               map[string]*semaphore{},
		leases:             map[LeaseID]*lease{},
		tokens:             map[string]LeaseID{},
		memberOf:           map[string]LeaseID{},
		tokenGen:           gen,
	}, nil
}

func randomToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("admission: token entropy unavailable: %v", err))
	}
	return hex.EncodeToString(buf)
}

func toMilliCores(cores float64) (int64, error) {
	if math.IsNaN(cores) || math.IsInf(cores, 0) || cores < 0 {
		return 0, fmt.Errorf("%w: cores %v", ErrInvalidRequest, cores)
	}
	m := math.Round(cores * 1000)
	if m > float64(maxMilliCores) {
		return 0, fmt.Errorf("%w: cores %v out of range", ErrInvalidRequest, cores)
	}
	return int64(m), nil
}

// Submit decides one admission request. The outcome is exactly one of:
// granted (with a fresh lease, superseding holders where
// [PolicyCancelOthers] claims required it), queued (holding nothing),
// failed or skipped (a blocked [PolicyFail] / [PolicySkip] claim, checked
// in claim order), or an error for requests that are malformed
// ([ErrInvalidRequest]), can never fit ([ErrNeverAdmissible]), or name a
// participant already tracked ([ErrDuplicateID]).
//
// A fail or skip claim is judged at submit time against its semaphore's
// instantaneous budget and FIFO queue; once a request queues on other
// resources, its fail and skip claims hold their FIFO position like queue
// claims.
func (l *Ledger) Submit(req Request) (Decision, []Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	s, err := l.normalize(req)
	if err != nil {
		return Decision{}, nil, err
	}
	if err := l.checkFreshID(s.id); err != nil {
		return Decision{}, nil, err
	}

	if !l.fifoBlocked(s) && l.fits(s) {
		l.admitSeq++
		s.admit = l.admitSeq
		grantedLease, evicted, events := l.grant(s, EventGranted)
		events = append(events, l.promote()...)
		l.mustHoldInvariants()
		return Decision{Kind: DecisionGranted, Lease: grantedLease, Evicted: evicted}, events, nil
	}

	for _, c := range s.claims {
		if c.policy != PolicyFail && c.policy != PolicySkip {
			continue
		}
		if l.fifoBlockedOnKey(c.key) || !l.semBudgetFits(c) {
			kind := DecisionFailed
			if c.policy == PolicySkip {
				kind = DecisionSkipped
			}
			l.mustHoldInvariants()
			return Decision{Kind: kind, Key: c.key}, nil, nil
		}
	}

	l.admitSeq++
	s.admit = l.admitSeq
	position := l.queuePosition(s)
	l.arrivalSeq++
	l.waiters = append(l.waiters, &waiter{arrival: l.arrivalSeq, spec: s})
	ev := l.newEvent(EventQueued, s.id)
	ev.Position = position
	l.mustHoldInvariants()
	return Decision{Kind: DecisionQueued, Position: position}, []Event{ev}, nil
}

// ReplaceWaiter updates an existing queued participant before it is
// granted. The waiter keeps its FIFO arrival, but its resource demand is
// the latest request. If the new demand now fits, eligible waiters are
// promoted.
func (l *Ledger) ReplaceWaiter(req Request) ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	s, err := l.normalize(req)
	if err != nil {
		return nil, err
	}
	for _, le := range l.leases {
		if _, ok := le.members[s.id]; ok {
			return nil, ErrDuplicateID
		}
	}
	for _, w := range l.waiters {
		if w.spec.id != s.id {
			continue
		}
		s.admit = w.spec.admit
		w.spec = s
		events := l.promote()
		l.mustHoldInvariants()
		return events, nil
	}
	return nil, ErrUnknownMember
}

// Attach joins memberID to a live lease, drawing zero new budget. The
// lease stays alive until every member, the original requester included,
// has released.
func (l *Ledger) Attach(id LeaseID, memberID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if memberID == "" {
		return fmt.Errorf("%w: empty member id", ErrInvalidRequest)
	}
	le, ok := l.leases[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownLease, id)
	}
	if err := l.checkFreshID(memberID); err != nil {
		return err
	}
	le.members[memberID] = struct{}{}
	l.memberOf[memberID] = id
	l.mustHoldInvariants()
	return nil
}

// Release removes memberID from the lease. When the last member leaves,
// every resource the lease held is freed, a released event is emitted,
// and eligible waiters are promoted. Releasing a superseded hold frees
// no semaphore budget (its budget was already reassigned at eviction)
// but still frees the lease's host resources.
func (l *Ledger) Release(id LeaseID, memberID string) ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	le, ok := l.leases[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownLease, id)
	}
	if _, ok := le.members[memberID]; !ok {
		return nil, fmt.Errorf("%w: %q on %s", ErrUnknownMember, memberID, id)
	}
	delete(le.members, memberID)
	delete(l.memberOf, memberID)
	if len(le.members) > 0 {
		l.mustHoldInvariants()
		return nil, nil
	}

	delete(l.leases, id)
	delete(l.tokens, le.token)
	l.usedMilliCores -= le.milliCores
	l.usedMemory -= le.memory
	for _, c := range le.claims {
		l.dropHold(c.key, id)
	}
	ev := l.newEvent(EventReleased, le.requestID)
	ev.Lease = id
	events := append([]Event{ev}, l.promote()...)
	l.mustHoldInvariants()
	return events, nil
}

// Reattach resolves a re-attach token to its live lease, for clients
// reclaiming their admission after a daemon takeover or reconnect. It
// mutates nothing.
func (l *Ledger) Reattach(token string) (LeaseID, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	id, ok := l.tokens[token]
	if !ok {
		return "", ErrUnknownToken
	}
	return id, nil
}

// LeaseByID returns the full lease (ID plus re-attach token) for a live
// lease ID, for delivering credentials to a promoted waiter. The second
// return is false when the lease is not live.
func (l *Ledger) LeaseByID(id LeaseID) (Lease, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	le, ok := l.leases[id]
	if !ok {
		return Lease{}, false
	}
	return Lease{ID: le.id, Token: le.token}, true
}

// SetHeadroom injects the load-sensed available host capacity. The
// effective host budget for new admissions is the minimum of the totals
// and the headroom. Shrinking headroom never evicts existing grants; it
// only gates new ones. Raising it promotes eligible waiters.
func (l *Ledger) SetHeadroom(cores float64, memoryBytes uint64) ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	mc, err := toMilliCores(cores)
	if err != nil {
		return nil, fmt.Errorf("%w: cores %v", ErrInvalidHeadroom, cores)
	}
	l.headroomMilliCores = mc
	l.headroomMemory = memoryBytes
	events := l.promote()
	l.mustHoldInvariants()
	return events, nil
}

// ResizeTotals replaces the fixed host capacity after a daemon restore
// on a machine with different effective limits. It preserves current
// grants and waiters. Memory remains a hard safety bound; CPU overcommit
// is tolerated for existing grants and drains before new CPU work admits.
func (l *Ledger) ResizeTotals(cores float64, memoryBytes uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	mc, err := toMilliCores(cores)
	if err != nil {
		return fmt.Errorf("%w: cores %v", ErrInvalidResize, cores)
	}
	if l.usedMemory > memoryBytes {
		return fmt.Errorf("%w: granted memory %d exceeds total %d", ErrInvalidResize, l.usedMemory, memoryBytes)
	}
	for _, w := range l.waiters {
		if w.spec.memory > memoryBytes {
			return fmt.Errorf("%w: waiter %q memory %d exceeds total %d", ErrInvalidResize, w.spec.id, w.spec.memory, memoryBytes)
		}
		if w.spec.strictCores && w.spec.milliCores > mc {
			return fmt.Errorf("%w: strict waiter %q cores %d exceed total %d", ErrInvalidResize, w.spec.id, w.spec.milliCores, mc)
		}
	}

	l.totalMilliCores = mc
	l.totalMemory = memoryBytes
	l.headroomMilliCores = min(l.headroomMilliCores, mc)
	l.headroomMemory = min(l.headroomMemory, memoryBytes)
	for _, w := range l.waiters {
		w.spec.milliCores = min(w.spec.milliCores, mc)
	}
	l.mustHoldInvariants()
	return nil
}

func (l *Ledger) normalize(req Request) (spec, error) {
	if req.ID == "" {
		return spec{}, fmt.Errorf("%w: empty request id", ErrInvalidRequest)
	}
	mc, err := toMilliCores(req.Cores)
	if err != nil {
		return spec{}, err
	}
	s := spec{
		id:          req.ID,
		milliCores:  mc,
		softCores:   req.SoftCores,
		strictCores: req.StrictCores,
		memory:      req.MemoryBytes,
	}
	seen := make(map[string]bool, len(req.Semaphores))
	for _, c := range req.Semaphores {
		nc, err := normalizeClaim(c.Key, c.Capacity, c.Cost, c.Policy)
		if err != nil {
			return spec{}, err
		}
		if seen[nc.key] {
			return spec{}, fmt.Errorf("%w: duplicate semaphore key %q", ErrInvalidRequest, nc.key)
		}
		seen[nc.key] = true
		s.claims = append(s.claims, nc)
	}
	if s.milliCores > l.totalMilliCores {
		if s.strictCores {
			return spec{}, fmt.Errorf("%w: cores %d exceed total %d", ErrNeverAdmissible, s.milliCores, l.totalMilliCores)
		}
		s.milliCores = l.totalMilliCores
	}
	if s.memory > l.totalMemory {
		return spec{}, fmt.Errorf("%w: memory %d exceeds total %d", ErrNeverAdmissible, s.memory, l.totalMemory)
	}
	return s, nil
}

func normalizeClaim(key string, capacity, cost int, policy Policy) (claim, error) {
	if key == "" {
		return claim{}, fmt.Errorf("%w: empty semaphore key", ErrInvalidRequest)
	}
	if capacity < 1 {
		return claim{}, fmt.Errorf("%w: semaphore %q capacity %d, must be at least 1", ErrInvalidRequest, key, capacity)
	}
	if cost < 0 {
		return claim{}, fmt.Errorf("%w: semaphore %q cost %d, must be non-negative", ErrInvalidRequest, key, cost)
	}
	if policy == "" {
		policy = PolicyQueue
	}
	switch policy {
	case PolicyQueue, PolicyFail, PolicySkip, PolicyCancelOthers:
	default:
		return claim{}, fmt.Errorf("%w: semaphore %q policy %q", ErrInvalidRequest, key, policy)
	}
	if cost > capacity {
		return claim{}, fmt.Errorf("%w: semaphore %q cost %d exceeds capacity %d", ErrNeverAdmissible, key, cost, capacity)
	}
	return claim{key: key, capacity: capacity, cost: cost, policy: policy}, nil
}

func (l *Ledger) checkFreshID(id string) error {
	if _, ok := l.memberOf[id]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicateID, id)
	}
	for _, w := range l.waiters {
		if w.spec.id == id {
			return fmt.Errorf("%w: %q", ErrDuplicateID, id)
		}
	}
	return nil
}

func (s spec) fifoResources() []resource {
	var rs []resource
	if s.milliCores > 0 {
		rs = append(rs, resourceCores)
	}
	if s.memory > 0 {
		rs = append(rs, resourceMemory)
	}
	for _, c := range s.claims {
		if c.policy != PolicyCancelOthers {
			rs = append(rs, semResource(c.key))
		}
	}
	return rs
}

// fifoBlocked reports whether a fresh arrival must queue behind the
// existing waiters rather than backfill ahead of them. It touches a
// blocked resource when an earlier waiter is either currently runnable or
// starved on that resource by holders younger than the waiter; a resource
// held only against older holders is left open for backfill.
func (l *Ledger) fifoBlocked(s spec) bool {
	_, blocked := l.scanWaiters(false)
	return anyIn(blocked, s.fifoResources())
}

// scanWaiters walks queued waiters in arrival order and computes the set
// of resources on which promotion is blocked. A waiter that does not fit
// blocks only the resources on which holders younger than it are what keep
// it from fitting, so a later waiter can backfill a resource that only
// older holders occupy. A waiter that touches an already-blocked resource
// yields all its resources to preserve per-resource FIFO order. When
// findFit is set, the index of the first waiter that both fits and is not
// blocked is returned; otherwise the accumulated blocked set is returned.
func (l *Ledger) scanWaiters(findFit bool) (int, map[resource]bool) {
	blocked := map[resource]bool{}
	for i, w := range l.waiters {
		rs := w.spec.fifoResources()
		if anyIn(blocked, rs) {
			markAll(blocked, rs)
			continue
		}
		if l.fits(w.spec) {
			if findFit {
				return i, blocked
			}
			markAll(blocked, rs)
			continue
		}
		for _, r := range rs {
			if l.starvedByYounger(w.spec, r) {
				blocked[r] = true
			}
		}
	}
	return -1, blocked
}

// starvedByYounger reports whether waiter w cannot currently claim
// resource r, yet would fit if holders admitted after w arrived were set
// aside. When true, younger backfilled holders -- not older ones w queued
// behind -- are what keep w from running, so w's claim on r is protected
// and later waiters must not backfill past it.
func (l *Ledger) starvedByYounger(w spec, r resource) bool {
	demand, used, usedOlder, capacity, ok := l.resourceBudget(w, r)
	if !ok {
		return false
	}
	if fitsCost(used, demand, capacity) {
		return false
	}
	if r == resourceCores && w.softCores && demand > capacity {
		return true
	}
	return w.fitsResourceWithOlder(r, usedOlder, demand, capacity)
}

func (s spec) fitsResourceWithOlder(r resource, usedOlder, demand, capacity int64) bool {
	if r == resourceCores && s.softCores && demand > 0 && usedOlder == 0 {
		return true
	}
	return fitsCost(usedOlder, demand, capacity)
}

// resourceBudget returns waiter w's demand on resource r, the live cost
// against it, the cost owed only to holders no younger than w, and r's
// effective capacity. ok is false for a resource w does not weigh on
// (a cancel_others claim, or a semaphore w does not queue on).
func (l *Ledger) resourceBudget(w spec, r resource) (demand, used, usedOlder, capacity int64, ok bool) {
	switch r {
	case resourceCores:
		capacity = min(l.totalMilliCores, l.headroomMilliCores)
		used = l.usedMilliCores
		for _, le := range l.leases {
			if le.admit <= w.admit {
				usedOlder += le.milliCores
			}
		}
		return w.milliCores, used, usedOlder, capacity, true
	case resourceMemory:
		capacity = int64(min(l.totalMemory, l.headroomMemory))
		used = int64(l.usedMemory)
		for _, le := range l.leases {
			if le.admit <= w.admit {
				usedOlder += int64(le.memory)
			}
		}
		return int64(w.memory), used, usedOlder, capacity, true
	default:
		key := semKeyOf(r)
		c, found := w.claim(key)
		if !found || c.policy == PolicyCancelOthers {
			return 0, 0, 0, 0, false
		}
		capacity = int64(l.semEffectiveCapacity(key, c.capacity))
		if sem := l.sems[key]; sem != nil {
			for _, h := range sem.holds {
				if h.superseded {
					continue
				}
				used += int64(h.cost)
				if le, live := l.leases[h.lease]; live && le.admit <= w.admit {
					usedOlder += int64(h.cost)
				}
			}
		}
		return int64(c.cost), used, usedOlder, capacity, true
	}
}

// fitsCost reports whether a demand of cost fits in capacity given used,
// comparing by subtraction so a large declared cost cannot overflow the
// sum into a false fit.
func fitsCost(used, cost, capacity int64) bool {
	return used <= capacity && cost <= capacity-used
}

func semKeyOf(r resource) string { return strings.TrimPrefix(string(r), "semaphore:") }

func (s spec) claim(key string) (claim, bool) {
	for _, c := range s.claims {
		if c.key == key {
			return c, true
		}
	}
	return claim{}, false
}

func (l *Ledger) fifoBlockedOnKey(key string) bool {
	mine := map[resource]bool{semResource(key): true}
	for _, w := range l.waiters {
		if touchesAny(w.spec, mine) {
			return true
		}
	}
	return false
}

func (l *Ledger) queuePosition(s spec) int {
	mine := resourceSet(s.fifoResources())
	n := 0
	for _, w := range l.waiters {
		if touchesAny(w.spec, mine) {
			n++
		}
	}
	return n
}

func resourceSet(rs []resource) map[resource]bool {
	set := make(map[resource]bool, len(rs))
	for _, r := range rs {
		set[r] = true
	}
	return set
}

func touchesAny(s spec, set map[resource]bool) bool {
	for _, r := range s.fifoResources() {
		if set[r] {
			return true
		}
	}
	return false
}

// hostFits reports whether a spec's host cores and memory fit right now.
// Memory is always a hard safety budget. A soft CPU request uses cores as
// backpressure: it limits additional admissions once the host is already
// overcommitted, but it never turns a memory-fitting head run into a
// permanent CPU-only wait.
func (l *Ledger) hostFits(s spec) bool {
	effMemory := min(l.totalMemory, l.headroomMemory)
	effCores := min(l.totalMilliCores, l.headroomMilliCores)
	coresOK := s.milliCores == 0 || l.usedMilliCores == 0 ||
		(l.usedMilliCores <= effCores && s.milliCores <= effCores-l.usedMilliCores)
	if s.softCores {
		coresOK = l.coresFitSoft(s)
	}
	memoryOK := s.memory == 0 || (l.usedMemory <= effMemory && s.memory <= effMemory-l.usedMemory)
	return coresOK && memoryOK
}

func (l *Ledger) coresFitSoft(s spec) bool {
	if s.milliCores == 0 || l.usedMilliCores == 0 {
		return true
	}
	effCores := min(l.totalMilliCores, l.headroomMilliCores)
	return fitsCost(l.usedMilliCores, s.milliCores, effCores)
}

func (l *Ledger) semUsed(key string) int {
	sem := l.sems[key]
	if sem == nil {
		return 0
	}
	used := 0
	for _, h := range sem.holds {
		if !h.superseded {
			used += h.cost
		}
	}
	return used
}

func (l *Ledger) semEffectiveCapacity(key string, incoming int) int {
	eff := 0
	sem := l.sems[key]
	if sem != nil {
		for _, h := range sem.holds {
			if !h.superseded && (eff == 0 || h.capacity < eff) {
				eff = h.capacity
			}
		}
	}
	if incoming > 0 && (eff == 0 || incoming < eff) {
		eff = incoming
	}
	if eff > 0 {
		return eff
	}
	if sem != nil && sem.lastCapacity > 0 {
		return sem.lastCapacity
	}
	return 1
}

// semBudgetFits compares by subtraction (cost <= capacity-used) rather
// than summing used+cost, so a huge declared cost cannot overflow the
// sum into a false fit.
func (l *Ledger) semBudgetFits(c claim) bool {
	used := l.semUsed(c.key)
	eff := l.semEffectiveCapacity(c.key, c.capacity)
	return used <= eff && c.cost <= eff-used
}

func (l *Ledger) claimFits(c claim) bool {
	if c.policy == PolicyCancelOthers {
		return true
	}
	return l.semBudgetFits(c)
}

func (l *Ledger) fits(s spec) bool {
	if !l.hostFits(s) {
		return false
	}
	for _, c := range s.claims {
		if !l.claimFits(c) {
			return false
		}
	}
	return true
}

func (l *Ledger) grant(s spec, kind EventKind) (Lease, []LeaseID, []Event) {
	l.leaseSeq++
	id := LeaseID(fmt.Sprintf("lease-%d", l.leaseSeq))
	token := l.mintToken()
	var events []Event
	var evicted []LeaseID
	for _, c := range s.claims {
		victims, evictEvents := l.evictForClaim(c, id)
		evicted = append(evicted, victims...)
		events = append(events, evictEvents...)
		sem := l.sems[c.key]
		if sem == nil {
			sem = &semaphore{}
			l.sems[c.key] = sem
		}
		sem.lastCapacity = c.capacity
		sem.holds = append(sem.holds, &hold{lease: id, cost: c.cost, capacity: c.capacity})
	}
	l.usedMilliCores += s.milliCores
	l.usedMemory += s.memory
	l.leases[id] = &lease{
		seq:         l.leaseSeq,
		admit:       s.admit,
		id:          id,
		token:       token,
		requestID:   s.id,
		milliCores:  s.milliCores,
		softCores:   s.softCores,
		strictCores: s.strictCores,
		memory:      s.memory,
		claims:      s.claims,
		members:     map[string]struct{}{s.id: {}},
	}
	l.tokens[token] = id
	l.memberOf[s.id] = id
	ev := l.newEvent(kind, s.id)
	ev.Lease = id
	events = append(events, ev)
	return Lease{ID: id, Token: token}, evicted, events
}

func (l *Ledger) evictForClaim(c claim, by LeaseID) ([]LeaseID, []Event) {
	if c.policy != PolicyCancelOthers {
		return nil, nil
	}
	var victims []LeaseID
	var events []Event
	for !l.semBudgetFits(c) {
		h := l.oldestLiveHold(c.key)
		if h == nil {
			panic(fmt.Sprintf("admission: semaphore %q cannot fit cost %d after evicting every holder", c.key, c.cost))
		}
		h.superseded = true
		h.supersededBy = by
		ev := l.newEvent(EventEvicted, l.leases[h.lease].requestID)
		ev.Lease = h.lease
		ev.Key = c.key
		ev.SupersededBy = by
		victims = append(victims, h.lease)
		events = append(events, ev)
	}
	return victims, events
}

func (l *Ledger) oldestLiveHold(key string) *hold {
	sem := l.sems[key]
	if sem == nil {
		return nil
	}
	for _, h := range sem.holds {
		if !h.superseded {
			return h
		}
	}
	return nil
}

func (l *Ledger) dropHold(key string, id LeaseID) {
	sem := l.sems[key]
	if sem == nil {
		return
	}
	var kept []*hold
	for _, h := range sem.holds {
		if h.lease != id {
			kept = append(kept, h)
		}
	}
	sem.holds = kept
	if len(sem.holds) == 0 {
		delete(l.sems, key)
	}
}

// promote grants waiters until no further waiter is admissible. Each pass
// scans in arrival order and grants the oldest waiter that fits and is not
// blocked: a waiter that does not fit is backfilled past only while older
// holders are what block it, and once holders younger than it are the
// blocker its resources are protected so no later waiter starves it.
func (l *Ledger) promote() []Event {
	var events []Event
	for {
		i := l.nextPromotable()
		if i < 0 {
			return events
		}
		w := l.waiters[i]
		l.waiters = append(l.waiters[:i], l.waiters[i+1:]...)
		_, _, grantEvents := l.grant(w.spec, EventPromoted)
		events = append(events, grantEvents...)
	}
}

func (l *Ledger) nextPromotable() int {
	i, _ := l.scanWaiters(true)
	return i
}

func anyIn(set map[resource]bool, rs []resource) bool {
	for _, r := range rs {
		if set[r] {
			return true
		}
	}
	return false
}

func markAll(set map[resource]bool, rs []resource) {
	for _, r := range rs {
		set[r] = true
	}
}

func (l *Ledger) mintToken() string {
	for range 100 {
		t := l.tokenGen()
		if t == "" {
			panic("admission: token generator returned an empty token")
		}
		if _, taken := l.tokens[t]; !taken {
			return t
		}
	}
	panic("admission: token generator returned 100 duplicate tokens")
}

func (l *Ledger) newEvent(kind EventKind, requestID string) Event {
	l.eventSeq++
	return Event{Seq: l.eventSeq, Kind: kind, RequestID: requestID}
}
