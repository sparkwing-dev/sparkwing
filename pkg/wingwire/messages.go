package wingwire

// Hello is the client's opening message on a fresh connection: its
// protocol major and binary version. The daemon answers with
// [HelloAck]. A client whose ProtocolMajor is ahead of the daemon's
// initiates a takeover (drain the old daemon, spawn its own binary);
// a client behind the daemon's major fails with an upgrade message.
type Hello struct {
	ProtocolMajor int    `json:"protocol_major"`
	BinaryVersion string `json:"binary_version"`
}

// HelloAck is the daemon's reply to [Hello], carrying the daemon's own
// protocol major and binary version. Draining reports that the daemon
// has stopped admitting new work (a takeover is in progress): existing
// leases keep being served, but a client that needs admission should
// wait for the successor daemon and reconnect.
type HelloAck struct {
	ProtocolMajor int    `json:"protocol_major"`
	BinaryVersion string `json:"binary_version"`
	Draining      bool   `json:"draining,omitempty"`
}

// HostResources is an amount of machine capacity: CPU cores and
// resident memory. It appears both as a claim (what a run needs) and
// as an accounting figure (what a holder is charged).
type HostResources struct {
	// Cores is a number of CPU cores; fractional values are meaningful.
	Cores float64 `json:"cores,omitempty"`
	// MemoryBytes is resident memory in bytes.
	MemoryBytes int64 `json:"memory_bytes,omitempty"`
}

// Policy is what a run does when a resource or semaphore it needs is
// at capacity. The values mirror the SDK's OnLimit set.
type Policy string

const (
	// PolicyQueue waits in FIFO order for room.
	PolicyQueue Policy = "queue"
	// PolicyFail errors the run immediately.
	PolicyFail Policy = "fail"
	// PolicySkip resolves the run as a no-op without running it.
	PolicySkip Policy = "skip"
	// PolicyCancelOthers evicts running holders oldest-first until the
	// requester fits, then admits it.
	PolicyCancelOthers Policy = "cancel_others"
)

// SemaphoreClaim is one named logical semaphore a run needs, with the
// cost it draws, the capacity it declares, and its at-limit policy.
// Capacity travels with the claim because semaphores are declared by
// pipelines, not configured on the daemon; when live claimants declare
// different capacities for the same name the daemon takes the minimum.
type SemaphoreClaim struct {
	Name     string `json:"name"`
	Cost     int    `json:"cost"`
	Capacity int    `json:"capacity"`
	Policy   Policy `json:"policy"`
	// QueueTimeoutMS bounds a PolicyQueue wait in milliseconds; zero
	// waits indefinitely.
	QueueTimeoutMS int64 `json:"queue_timeout_ms,omitempty"`
}

// AdmissionRequest asks the daemon for everything a run needs in one
// all-or-nothing grant: host resources plus every logical semaphore.
// There is no partial admission and no ordered-acquisition dance; the
// daemon answers with [Grant], a stream of [Queued] positions followed
// by a [Grant], or an [Evicted] terminal event, per the policies.
type AdmissionRequest struct {
	RunID string `json:"run_id"`
	// Resources is the host capacity the run is expected to occupy.
	// A zero value means the run declared no hints and the daemon
	// charges its conservative default.
	Resources  HostResources    `json:"resources"`
	Semaphores []SemaphoreClaim `json:"semaphores,omitempty"`
	// ParentLeaseToken attaches this run to a live parent lease (see
	// [LeaseTokenEnv]) so nested runs are not double-charged. Empty for
	// top-level runs.
	ParentLeaseToken string `json:"parent_lease_token,omitempty"`
	// SemaphoresOnly marks a request that draws no host budget even when
	// Resources is zero: the daemon must not substitute its conservative
	// default charge. Used for short-lived semaphore acquisitions made
	// from inside an already-admitted run (node-level concurrency
	// groups).
	SemaphoresOnly bool `json:"semaphores_only,omitempty"`
}

// Grant is the daemon's admission of a request. The lease lives as
// long as the client's connection; LeaseToken additionally lets the
// holder [Reattach] within the grace window after a daemon restart or
// takeover, and lets child runs inherit via [LeaseTokenEnv].
type Grant struct {
	RunID      string `json:"run_id"`
	LeaseToken string `json:"lease_token"`
	// Resources is what the daemon actually charged: the request's
	// declared resources, or the daemon's default where the request
	// declared none.
	Resources HostResources `json:"resources"`
	// Semaphores names the semaphores the granted lease holds. On a
	// child attach this is the parent lease's full set, so the child
	// knows which of its own claims are already covered by the lease and
	// which it must acquire separately.
	Semaphores []string `json:"semaphores,omitempty"`
}

// Queued reports a waiting run's position whenever it changes. Key
// names what the run is waiting on -- a semaphore name, or a host
// resource dimension ("cores", "memory").
type Queued struct {
	RunID string `json:"run_id"`
	Key   string `json:"key"`
	// Position is 1-based; 1 is next to be admitted.
	Position    int `json:"position"`
	QueueLength int `json:"queue_length"`
}

// Evicted reports that a holder lost its lease to a
// [PolicyCancelOthers] requester. Terminal for the holder's admission:
// the run winds down cooperatively and must re-request if it wants to
// run again.
type Evicted struct {
	RunID string `json:"run_id"`
	Key   string `json:"key"`
	// SupersededBy is the run whose admission evicted this holder.
	SupersededBy string `json:"superseded_by"`
	Policy       Policy `json:"policy"`
}

// Release returns a lease before the connection closes -- the explicit
// counterpart of the implicit release the daemon performs when a
// holder's socket closes.
type Release struct {
	LeaseToken string `json:"lease_token"`
}

// Reattach presents a lease token on a fresh connection to resume a
// lease that survived a daemon restart or version takeover. Accepted
// only within the daemon's reconnect grace window; after that the
// lease is gone and the run must submit a new [AdmissionRequest].
type Reattach struct {
	LeaseToken string `json:"lease_token"`
}

// DrainRequest tells a daemon to stop admitting new work while
// continuing to serve existing leases. Sent by a newer client that is
// about to spawn its own binary as the successor daemon.
type DrainRequest struct {
	// SuccessorVersion is the binary version that will take over.
	SuccessorVersion string `json:"successor_version"`
}

// DrainAck confirms the daemon is draining. HoldersRemaining is the
// number of live leases still attached; the successor takes over the
// socket and durable state once the old daemon exits.
type DrainAck struct {
	HoldersRemaining int `json:"holders_remaining"`
}

// ResourceState is one capacity row in a [QueueState]: a host resource
// dimension ("cores", "memory") or a semaphore name, with its total
// capacity and the amount currently held.
type ResourceState struct {
	Key      string  `json:"key"`
	Capacity float64 `json:"capacity"`
	Held     float64 `json:"held"`
}

// Holder is one run currently holding admission, as reported in a
// [QueueState].
type Holder struct {
	RunID string `json:"run_id"`
	// ElapsedMS is how long the run has held its lease, in
	// milliseconds.
	ElapsedMS int64 `json:"elapsed_ms"`
	// Resources is the host capacity the holder is charged.
	Resources HostResources `json:"resources"`
	// Semaphores names the semaphores the holder occupies.
	Semaphores []string `json:"semaphores,omitempty"`
}

// Waiter is one run queued for admission, as reported in a
// [QueueState].
type Waiter struct {
	RunID      string        `json:"run_id"`
	Resources  HostResources `json:"resources"`
	Semaphores []string      `json:"semaphores,omitempty"`
	// WaitingMS is how long the run has been queued, in milliseconds.
	WaitingMS int64 `json:"waiting_ms,omitempty"`
}

// QueueState is the daemon's full accounting snapshot: every capacity
// row, every holder with its elapsed time and cost, and every waiter.
// Waiters appear in admission order -- index zero is admitted next.
// This is the payload behind the CLI's queue view.
type QueueState struct {
	Resources []ResourceState `json:"resources,omitempty"`
	Holders   []Holder        `json:"holders,omitempty"`
	Waiters   []Waiter        `json:"waiters,omitempty"`
}

func (*Hello) wireType() MessageType            { return TypeHello }
func (*HelloAck) wireType() MessageType         { return TypeHelloAck }
func (*AdmissionRequest) wireType() MessageType { return TypeAdmissionRequest }
func (*Grant) wireType() MessageType            { return TypeGrant }
func (*Queued) wireType() MessageType           { return TypeQueued }
func (*Evicted) wireType() MessageType          { return TypeEvicted }
func (*Release) wireType() MessageType          { return TypeRelease }
func (*Reattach) wireType() MessageType         { return TypeReattach }
func (*DrainRequest) wireType() MessageType     { return TypeDrainRequest }
func (*DrainAck) wireType() MessageType         { return TypeDrainAck }
func (*QueueState) wireType() MessageType       { return TypeQueueState }
