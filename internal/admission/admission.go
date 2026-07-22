// Package admission implements the unified admission ledger: one pure,
// in-memory arbiter for everything a run needs before it may execute --
// host cores, host memory, and named weighted semaphores.
//
// A [Request] names every resource at once and a grant is all-or-nothing:
// the request is either fully granted (a [Lease] holding every resource)
// or fully waiting, never holding one resource while queued on another.
// Waiting is strict FIFO per resource, weighted by cost; a heavy request
// at the head of a queue blocks lighter requests behind it on the same
// resource (predictability over utilization). Requests that share no
// resource with any waiter are admitted independently.
//
// Each semaphore claim carries a [Policy] deciding what happens when its
// semaphore cannot admit it immediately: wait ([PolicyQueue]), reject the
// whole request ([PolicyFail]), resolve the whole request as a no-op
// ([PolicySkip]), or supersede existing holders newest-wins
// ([PolicyCancelOthers]). Host resources always queue. Superseding marks
// the victims' holds as no longer drawing budget and emits eviction
// events; the victims stay tracked until their leases are released, and
// grace handling for them is the caller's job.
//
// A grant mints a [Lease] with an opaque ID and a re-attach token. Child
// requests attach to a parent lease with [Ledger.Attach], drawing zero
// new budget; the lease stays alive until every attached member has
// released. Host capacity is injected by the caller ([Config] totals plus
// the dynamic [Ledger.SetHeadroom] value); the ledger never samples the
// machine.
//
// Every mutation returns the [Event] values it produced, in deterministic
// order with a monotonic sequence, and re-asserts the ledger invariants,
// panicking on any violation. [Ledger.Snapshot] and [Restore] serialize
// and rebuild the full state for daemon takeover and crash recovery.
package admission

import "errors"

// Named errors returned by ledger operations. Every error a caller can
// branch on wraps one of these.
var (
	// ErrInvalidConfig reports a [Config] with non-finite or negative
	// capacity values.
	ErrInvalidConfig = errors.New("admission: invalid config")
	// ErrInvalidRequest reports a malformed [Request]: empty IDs, negative
	// or non-finite values, duplicate semaphore keys, or unknown policies.
	ErrInvalidRequest = errors.New("admission: invalid request")
	// ErrNeverAdmissible reports a request no release can satisfy: a
	// semaphore claim whose cost exceeds its own declared capacity, or host
	// memory demand above this machine's memory budget. Host CPU demand above
	// this machine's total is capped and serialized alone.
	ErrNeverAdmissible = errors.New("admission: request exceeds a semaphore's own capacity")
	// ErrDuplicateID reports a participant ID that already holds or waits.
	ErrDuplicateID = errors.New("admission: participant id already holds or waits")
	// ErrUnknownLease reports an operation against a lease ID the ledger
	// does not track.
	ErrUnknownLease = errors.New("admission: unknown lease")
	// ErrUnknownMember reports a release for a member not attached to the
	// lease.
	ErrUnknownMember = errors.New("admission: unknown lease member")
	// ErrUnknownToken reports a re-attach token that matches no live lease.
	ErrUnknownToken = errors.New("admission: unknown re-attach token")
	// ErrInvalidHeadroom reports a non-finite or negative headroom value.
	ErrInvalidHeadroom = errors.New("admission: invalid headroom")
	// ErrInvalidResize reports a total capacity change that would make
	// the current ledger state impossible.
	ErrInvalidResize = errors.New("admission: invalid resize")
	// ErrInvalidSnapshot reports a snapshot that fails validation or the
	// ledger invariants on restore.
	ErrInvalidSnapshot = errors.New("admission: invalid snapshot")
)

// Policy is what a semaphore claim does when its semaphore cannot admit
// it immediately. The zero value of a [SemaphoreClaim]'s Policy is
// [PolicyQueue].
type Policy string

const (
	// PolicyQueue waits in strict FIFO order for room.
	PolicyQueue Policy = "queue"
	// PolicyFail rejects the whole request immediately when the claim is
	// blocked, whether by budget or by earlier waiters.
	PolicyFail Policy = "fail"
	// PolicySkip resolves the whole request as skipped, without running,
	// when the claim is blocked.
	PolicySkip Policy = "skip"
	// PolicyCancelOthers supersedes existing holders oldest-first until
	// the claim fits, then admits immediately (newest wins). It never
	// waits on its own semaphore and is not blocked by that semaphore's
	// FIFO queue.
	PolicyCancelOthers Policy = "cancel_others"
)

// SemaphoreClaim is one named-semaphore hold a request needs: the
// coordination key, the capacity the requester declares for that key,
// the weighted cost the hold draws, and the policy applied when the
// semaphore cannot admit it.
//
// When live holders declare different capacities for the same key, the
// most restrictive declaration wins, so lowering a cap takes effect
// immediately and raising one waits for the lower declaration to drain.
type SemaphoreClaim struct {
	// Key is the coordination key. Requests naming the same key share one
	// budget. Must be non-empty and unique within a request.
	Key string
	// Capacity is the total budget this requester declares for the key,
	// in author-defined units. Must be at least 1.
	Capacity int
	// Cost is the admission weight this hold draws from the budget. Zero
	// is allowed and draws nothing but still respects FIFO order. Must
	// not exceed Capacity or the request is never admissible.
	Cost int
	// Policy is the at-limit behavior. Empty means [PolicyQueue].
	Policy Policy
}

// Request names everything one run needs admission for. Grants are
// all-or-nothing: either every listed resource is held by the resulting
// lease or the request waits holding nothing.
type Request struct {
	// ID identifies the requesting participant (a run). It becomes the
	// lease's first member on grant and must not already hold or wait.
	ID string
	// Cores is the host CPU demand in cores; fractional values are
	// allowed and are accounted in millicores. Zero requests no CPU.
	Cores float64
	// SoftCores makes CPU demand backpressure instead of a hard safety
	// budget. Memory and semaphores still gate admission strictly.
	SoftCores bool
	// StrictCores rejects CPU demand above this machine's CPU budget instead
	// of capping it down to run alone.
	StrictCores bool
	// MemoryBytes is the host memory demand. Zero requests no memory.
	MemoryBytes uint64
	// Semaphores are the named-semaphore holds the request needs.
	Semaphores []SemaphoreClaim
	// Priority orders queued work. Larger values admit before smaller
	// values; equal values keep FIFO order.
	Priority int
}

// LeaseID is the opaque identifier of a granted lease.
type LeaseID string

// Lease is a granted admission: the ID other operations reference and
// the re-attach token a client presents to reclaim the lease after a
// daemon takeover or reconnect.
type Lease struct {
	ID    LeaseID
	Token string
}

// DecisionKind classifies the outcome of [Ledger.Submit].
type DecisionKind string

const (
	// DecisionGranted means every resource was admitted and a lease was
	// created.
	DecisionGranted DecisionKind = "granted"
	// DecisionQueued means the request is waiting, holding nothing.
	DecisionQueued DecisionKind = "queued"
	// DecisionFailed means a blocked [PolicyFail] claim rejected the
	// whole request.
	DecisionFailed DecisionKind = "failed"
	// DecisionSkipped means a blocked [PolicySkip] claim resolved the
	// whole request as a no-op.
	DecisionSkipped DecisionKind = "skipped"
)

// Decision is the outcome of one [Ledger.Submit].
type Decision struct {
	// Kind classifies the outcome; the other fields are populated per
	// kind.
	Kind DecisionKind
	// Lease is the granted lease when Kind is [DecisionGranted].
	Lease Lease
	// Position is the number of earlier waiters this request must wait
	// behind on at least one shared resource when Kind is
	// [DecisionQueued]; 0 means next in line.
	Position int
	// Key names the semaphore whose policy produced [DecisionFailed] or
	// [DecisionSkipped].
	Key string
	// Evicted lists the leases superseded by this grant's
	// [PolicyCancelOthers] claims, oldest first.
	Evicted []LeaseID
}

// EventKind classifies a ledger transition event.
type EventKind string

const (
	// EventGranted reports a request admitted directly at submit.
	EventGranted EventKind = "granted"
	// EventQueued reports a request parked in the wait queue.
	EventQueued EventKind = "queued"
	// EventPromoted reports a waiter admitted after capacity opened.
	EventPromoted EventKind = "promoted"
	// EventEvicted reports a holder superseded by a
	// [PolicyCancelOthers] grant.
	EventEvicted EventKind = "evicted"
	// EventReleased reports a lease fully released (its last member
	// left).
	EventReleased EventKind = "released"
)

// Event is one ledger transition, emitted in deterministic order. Seq is
// monotonic across the ledger's lifetime and survives snapshot/restore.
type Event struct {
	// Seq is the monotonic event sequence number, starting at 1.
	Seq uint64 `json:"seq"`
	// Kind classifies the transition.
	Kind EventKind `json:"kind"`
	// RequestID is the participant the event concerns: the requester for
	// granted/queued/promoted, the victim lease's requester for evicted,
	// the released lease's requester for released.
	RequestID string `json:"request_id"`
	// Lease is the lease the event concerns; empty for queued.
	Lease LeaseID `json:"lease,omitempty"`
	// Position is the queue position for queued events; 0 means next in
	// line.
	Position int `json:"position,omitempty"`
	// Key names the contested semaphore for evicted events.
	Key string `json:"key,omitempty"`
	// SupersededBy is the superseding lease for evicted events.
	SupersededBy LeaseID `json:"superseded_by,omitempty"`
}
