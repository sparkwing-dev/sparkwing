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
	// CancelTimeoutMS bounds, in milliseconds, how long a
	// PolicyCancelOthers requester waits for the holders it superseded to
	// wind down cooperatively before the daemon force-releases their
	// leases. Zero leaves a non-cooperating holder to release on its own.
	CancelTimeoutMS int64 `json:"cancel_timeout_ms,omitempty"`
}

// AdmissionRequest asks the daemon for everything a run needs in one
// all-or-nothing grant: host resources plus every logical semaphore.
// There is no partial admission and no ordered-acquisition dance; the
// daemon answers with [Grant], a stream of [Queued] positions followed
// by a [Grant], or an [Evicted] terminal event, per the policies.
type AdmissionRequest struct {
	RunID string `json:"run_id"`
	// Pipeline is the pipeline name behind the run, carried purely for
	// display in the queue view. Empty for requests that have no
	// pipeline (a semaphores-only node acquisition inherits the run's).
	Pipeline string `json:"pipeline,omitempty"`
	// PID is the run process's operating-system process id. The daemon
	// samples this process's CPU at a slow cadence to flag a holder that
	// is alive but idle while waiters queue behind it. Zero disables
	// stall sampling for the holder.
	PID int `json:"pid,omitempty"`
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
	// CostSource names how Resources was resolved -- "pin", "measured", or
	// "default" -- so the queue view can show where a charge came from. The
	// daemon treats it as opaque display metadata.
	CostSource string `json:"cost_source,omitempty"`
	// ExpectedDurationMS is the pipeline's measured p50 run duration in
	// milliseconds, used by the daemon to estimate queue ETAs. Zero means
	// no measured duration exists yet, so the run contributes no ETA.
	ExpectedDurationMS int64 `json:"expected_duration_ms,omitempty"`
	// DriftWarning, when set, is a one-line note that this run's explicit
	// pin has drifted from its measured profile. The daemon echoes it into
	// the queue view; it never affects admission.
	DriftWarning string `json:"drift_warning,omitempty"`
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
	// BlockingReason is a one-line explanation of what the run is waiting
	// on -- naming needed versus available host capacity and external
	// load when host pressure is the cause. Empty for a pure arrival-order
	// wait or an older daemon.
	BlockingReason string `json:"blocking_reason,omitempty"`
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
// capacity and the amount currently held. For the host dimensions it also
// carries the live headroom arithmetic -- the reserved margin, the
// measured non-sparkwing load, and what remains grantable right now --
// so the queue view can explain a wait that free capacity alone cannot.
// These headroom fields are zero for semaphore rows and for older daemons
// that predate them.
type ResourceState struct {
	Key      string  `json:"key"`
	Capacity float64 `json:"capacity"`
	Held     float64 `json:"held"`
	// Reserved is the margin held back from admission (the headroom
	// reserve). Zero for semaphore rows.
	Reserved float64 `json:"reserved,omitempty"`
	// External is the measured load from processes the daemon did not
	// admit -- other apps, the OS. Zero for semaphore rows.
	External float64 `json:"external,omitempty"`
	// Available is what a new run can actually draw right now: capacity
	// minus the reserve, minus external load, minus what sparkwing
	// already holds, floored at zero. This, not Capacity-Held, is what
	// gates host admission. Zero for semaphore rows.
	Available float64 `json:"available,omitempty"`
}

// Holder is one run currently holding admission, as reported in a
// [QueueState].
type Holder struct {
	RunID string `json:"run_id"`
	// Pipeline is the pipeline name behind the run, for display. Empty
	// when the run did not report one.
	Pipeline string `json:"pipeline,omitempty"`
	// ElapsedMS is how long the run has held its lease, in
	// milliseconds.
	ElapsedMS int64 `json:"elapsed_ms"`
	// Resources is the host capacity the holder is charged.
	Resources HostResources `json:"resources"`
	// Semaphores names the semaphores the holder occupies.
	Semaphores []string `json:"semaphores,omitempty"`
	// CostSource names how Resources was resolved ("pin", "measured",
	// "default"). Empty for leases whose source did not survive a daemon
	// restart.
	CostSource string `json:"cost_source,omitempty"`
	// ExpectedDurationMS is the holder's measured p50 run duration; zero
	// when unknown. ETA uses it to estimate when the holder frees capacity.
	ExpectedDurationMS int64 `json:"expected_duration_ms,omitempty"`
	// DriftWarning, when set, notes that the holder's pin has drifted from
	// its measured profile.
	DriftWarning string `json:"drift_warning,omitempty"`
	// Stalled marks a holder that is alive but has consumed near-zero
	// CPU for a sustained window while runs wait behind it -- a likely
	// wedge. It is a flag only; the daemon never kills a holder.
	Stalled bool `json:"stalled,omitempty"`
	// Recovery is the exact command an operator runs to clear a stalled
	// holder. Set only when Stalled is true; it never names a
	// destructive host verb.
	Recovery string `json:"recovery,omitempty"`
}

// Waiter is one run queued for admission, as reported in a
// [QueueState]. Waiters appear in arrival order; Position is its
// 1-based place in that order.
type Waiter struct {
	RunID string `json:"run_id"`
	// Pipeline is the pipeline name behind the run, for display. Empty
	// when the run did not report one.
	Pipeline string `json:"pipeline,omitempty"`
	// Position is the waiter's 1-based place in arrival order; 1 is
	// admitted next.
	Position   int           `json:"position"`
	Resources  HostResources `json:"resources"`
	Semaphores []string      `json:"semaphores,omitempty"`
	// WaitingOn names the resources the waiter lacks room for right now
	// -- host dimensions ("cores", "memory") and full semaphore keys.
	// Empty means the waiter is held only by arrival order behind a
	// heavier request ahead of it.
	WaitingOn []string `json:"waiting_on,omitempty"`
	// BlockingReason is a one-line, human explanation of why this waiter
	// is not yet admitted, naming what it needs against what is available
	// and any external load ("needs 5.0 cores; 4.8 available (external
	// load 3.2)"). Empty when the wait is pure arrival-order queueing or
	// the daemon predates this field.
	BlockingReason string `json:"blocking_reason,omitempty"`
	// WaitingMS is how long the run has been queued, in milliseconds.
	WaitingMS int64 `json:"waiting_ms,omitempty"`
	// CostSource names how Resources was resolved ("pin", "measured",
	// "default").
	CostSource string `json:"cost_source,omitempty"`
	// ExpectedDurationMS is the waiter's measured p50 run duration; zero
	// when unknown.
	ExpectedDurationMS int64 `json:"expected_duration_ms,omitempty"`
	// DriftWarning, when set, notes that the waiter's pin has drifted from
	// its measured profile.
	DriftWarning string `json:"drift_warning,omitempty"`
	// ExpectedStartMS is the estimated wait until this run is admitted, in
	// milliseconds from now, computed by simulating the queue with measured
	// durations and costs. Nil when any run ahead lacks a measured duration,
	// so no fabricated ETA is shown.
	ExpectedStartMS *int64 `json:"expected_start_ms,omitempty"`
}

// QueueState is the daemon's full accounting snapshot: every capacity
// row, every holder with its elapsed time and cost, and every waiter.
// Waiters appear in admission order -- index zero is admitted next.
// This is the payload behind the CLI's queue view.
type QueueState struct {
	Resources []ResourceState `json:"resources,omitempty"`
	Holders   []Holder        `json:"holders,omitempty"`
	Waiters   []Waiter        `json:"waiters,omitempty"`
	// ExpectedClearMS is the estimated time until the queue fully drains,
	// in milliseconds from now. Nil when the estimate is unavailable
	// because some queued or holding run lacks a measured duration.
	ExpectedClearMS *int64 `json:"expected_clear_ms,omitempty"`
	// DaemonVersion is the serving daemon's binary version, for the
	// queue header. Empty when the daemon predates this field.
	DaemonVersion string `json:"daemon_version,omitempty"`
	// DaemonUptimeMS is how long the serving daemon has been up, in
	// milliseconds. Zero when unknown.
	DaemonUptimeMS int64 `json:"daemon_uptime_ms,omitempty"`
}

// CancelLease asks the daemon to cancel a local run it is arbitrating,
// by run id, on a dedicated control connection. The daemon signals the
// run's holding connection to wind down cleanly (the same path as an
// operator interrupt) and answers with [CancelLeaseAck]. It is the
// dashboard-free recovery path: the daemon, not a controller, knows the
// run and holds its connection.
type CancelLease struct {
	RunID string `json:"run_id"`
}

// CancelLeaseAck answers a [CancelLease]. Found reports whether the
// daemon knew the run and signalled it; when false the caller should
// fall back to the controller (the run is not local, or already done).
type CancelLeaseAck struct {
	Found bool `json:"found"`
}

// Cancel is the daemon's push to a run's holding connection telling it to
// wind down cleanly, as if it had received an operator interrupt. Reason
// is a short human phrase for the run's terminal record.
type Cancel struct {
	RunID  string `json:"run_id"`
	Reason string `json:"reason,omitempty"`
}

func (*CancelLease) wireType() MessageType      { return TypeCancelLease }
func (*CancelLeaseAck) wireType() MessageType   { return TypeCancelLeaseAck }
func (*Cancel) wireType() MessageType           { return TypeCancel }
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
