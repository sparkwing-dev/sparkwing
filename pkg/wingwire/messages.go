package wingwire

// Origin identifies who dispatched a run competing for admission on a
// box, so a shared daemon's queue can say whether a row is the operator's
// own local work or work a controller sent to a registered runner. It is
// display metadata only; every requester is equal before the ledger.
type Origin string

const (
	// OriginLocal marks a run launched on this box directly -- a CLI
	// `sparkwing run` or a local trigger. The default when a request names
	// no origin.
	OriginLocal Origin = "local"
	// OriginController marks controller-dispatched work claimed by a
	// registered runner on this box, admitted through the same daemon as
	// local work.
	OriginController Origin = "controller"
)

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

// CostSource names how a request's host resources were resolved before
// reaching the daemon.
type CostSource string

const (
	CostSourcePin       CostSource = "pin"
	CostSourceMeasured  CostSource = "measured"
	CostSourceDefault   CostSource = "default"
	CostSourceMeasuring CostSource = "measuring"
	CostSourceFloor     CostSource = "floor"
)

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

// AdmissionRequest asks the daemon for one admission lease. A request
// may combine host resources and logical semaphores, draw only host
// resources for a node, or draw only semaphores for a run-level claim.
// The daemon answers with [Grant], a stream of [Queued] positions
// followed by a [Grant], or an [Evicted] terminal event, per the
// policies.
type AdmissionRequest struct {
	RunID string `json:"run_id"`
	// OwnerRunID is the real run that owns an internal participant. Empty
	// means the participant is the run.
	OwnerRunID string `json:"owner_run_id,omitempty"`
	// DisplayRunID is the label queue views print for this participant.
	// Empty means display the owner run.
	DisplayRunID string `json:"display_run_id,omitempty"`
	// Pipeline is the pipeline name behind the run, carried purely for
	// display in the queue view. Empty for requests that have no
	// pipeline (a semaphores-only node acquisition inherits the run's).
	Pipeline string `json:"pipeline,omitempty"`
	// Repo is the short name of the repository the run was launched
	// from (the git toplevel basename), carried purely for display so a
	// shared daemon's queue can say whose work each row is. Empty when
	// the run started outside any repository.
	Repo string `json:"repo,omitempty"`
	// PID is the run process's operating-system process id. The daemon
	// samples this process's CPU at a slow cadence to flag a holder that
	// is alive but idle while waiters queue behind it. Zero disables
	// stall sampling for the holder.
	PID int `json:"pid,omitempty"`
	// Resources is the host capacity the lease is expected to occupy. A
	// zero value means the request declared no hints and the daemon
	// charges its conservative default unless SemaphoresOnly is set.
	Resources  HostResources    `json:"resources"`
	Semaphores []SemaphoreClaim `json:"semaphores,omitempty"`
	// ParentLeaseToken attaches this run to a live parent lease (see
	// [LeaseTokenEnv]) so nested runs are not double-charged. Empty for
	// top-level runs.
	ParentLeaseToken string `json:"parent_lease_token,omitempty"`
	// SemaphoresOnly marks a request that draws no host budget even when
	// Resources is zero: the daemon must not substitute its conservative
	// default charge. Used for run-level semaphore claims and short-lived
	// semaphore acquisitions made from inside an already-admitted run.
	SemaphoresOnly bool `json:"semaphores_only,omitempty"`
	// SubLease marks an internal lease whose parent run owns finalization.
	// The daemon releases it on disconnect but never finalizes a run row for it.
	SubLease bool `json:"sub_lease,omitempty"`
	// CostSource names how Resources was resolved so the queue view can show
	// where a charge came from. The daemon may cap measured costs to the
	// largest idle-grantable request.
	CostSource CostSource `json:"cost_source,omitempty"`
	// ExpectedDurationMS is the admitted work item's measured p50 duration
	// in milliseconds, used by the daemon to estimate queue ETAs. Zero means
	// no measured duration exists yet, so the request contributes no ETA.
	ExpectedDurationMS int64 `json:"expected_duration_ms,omitempty"`
	// ExpectedP99MS is the admitted work item's measured p99 duration in
	// milliseconds. The daemon flags a holder as contended only once its
	// elapsed time runs well past this baseline, so work that is merely at
	// the slow end of its own distribution is never mistaken for throttled.
	// Zero means no measured p99, which disqualifies the holder from flagging.
	ExpectedP99MS int64 `json:"expected_p99_ms,omitempty"`
	// SampleCount is how many runs back the duration percentiles. The
	// contention detector requires a minimum count so an unprofiled or
	// barely-profiled run is never flagged.
	SampleCount int `json:"sample_count,omitempty"`
	// DriftWarning, when set, is a one-line note that this run's explicit
	// pin has drifted from its measured profile. The daemon echoes it into
	// the queue view; it never affects admission.
	DriftWarning string `json:"drift_warning,omitempty"`
	// Origin names who dispatched this run -- the operator's own local work
	// or a controller that sent it to a registered runner on this box. Empty
	// is treated as [OriginLocal]. Display metadata only; the daemon treats
	// every requester equally.
	Origin Origin `json:"origin,omitempty"`
	// Priority orders queued work. Larger values admit before smaller
	// values; equal values keep FIFO order.
	Priority int `json:"priority,omitempty"`
}

// Grant is the daemon's admission of a request. The lease lives as
// long as the client's connection; LeaseToken additionally lets the
// holder [Reattach] within the grace window after a daemon restart or
// takeover. Child runs inherit it via [LeaseTokenEnv], or inherit a
// separate child-attach token via [ChildLeaseTokenEnv] when the current
// execution lease differs from the run-scope lease.
type Grant struct {
	RunID      string `json:"run_id"`
	LeaseToken string `json:"lease_token"`
	// Resources is what the daemon actually charged: the request's
	// declared resources, a capped measured charge, or the daemon's
	// default where the request declared none.
	Resources HostResources `json:"resources"`
	// Semaphores names the semaphores the granted lease holds. On a
	// child attach this is the parent lease's full set, so the child
	// knows which of its own claims are already covered by the lease and
	// which it must acquire separately.
	Semaphores []string `json:"semaphores,omitempty"`
	// SoleRunUnderLoad is set when the liveness floor is what admitted this
	// run: the box was otherwise idle of sparkwing work but under enough
	// external load or reserve that only a sole run could fit. The client
	// narrates that additional runs will queue. Zero for a normal admission.
	SoleRunUnderLoad bool `json:"sole_run_under_load,omitempty"`
	// ExternalCores is the measured non-sparkwing load in cores at the moment
	// of a SoleRunUnderLoad grant, for the narration. Zero otherwise.
	ExternalCores float64 `json:"external_cores,omitempty"`
}

// Queued reports a waiting run's position whenever it changes. Key names
// the semaphore whose live capacity blocks the run. Host pressure is
// reported through BlockingReason.
type Queued struct {
	RunID string `json:"run_id"`
	Key   string `json:"key"`
	// Position is 1-based; 1 is next to be admitted.
	Position    int `json:"position"`
	QueueLength int `json:"queue_length"`
	// BlockingReason is a one-line explanation of what the run is waiting
	// on -- naming needed versus available host capacity and external
	// load when host pressure is the cause. Empty for a pure admission-order
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
	// Reason is a one-line human explanation naming the offending input
	// and its value when the daemon rejects a request as malformed, so the
	// client surfaces a cause rather than a bare policy key. Empty for
	// ordinary cancel_others evictions and older daemons.
	Reason string `json:"reason,omitempty"`
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
	// ParticipantID is the daemon lease key when it differs from RunID.
	ParticipantID string `json:"participant_id,omitempty"`
	// DisplayRunID is the label queue views print for this row. Empty
	// means display RunID.
	DisplayRunID string `json:"display_run_id,omitempty"`
	// Pipeline is the pipeline name behind the run, for display. Empty
	// when the run did not report one.
	Pipeline string `json:"pipeline,omitempty"`
	// Repo is the short repo name the run was launched from, for
	// display. Empty when the run did not report one (a non-git launch
	// directory, or a lease that survived a daemon restart).
	Repo string `json:"repo,omitempty"`
	// Parent, when non-empty, names the holder this run is attached to:
	// the run rides its parent's lease and draws no budget of its own.
	Parent string `json:"parent,omitempty"`
	// ParentParticipantID is the daemon lease key for Parent when that
	// key differs from Parent.
	ParentParticipantID string `json:"parent_participant_id,omitempty"`
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
	// CostRationale is the short human phrase explaining that CostSource
	// ("measured p95 over 12 runs", "explicit pin"), for a dashboard to
	// tooltip beside the charge. Empty when the source is unknown.
	CostRationale string `json:"cost_rationale,omitempty"`
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
	// Contended marks a holder that is measurably slower than its profile
	// while the host is saturated -- throttled by contention rather than
	// wedged (which is Stalled) or legitimately long. It is a flag only;
	// the daemon never acts on a contended holder.
	Contended bool `json:"contended,omitempty"`
	// ContentionReason is a one-line explanation set when Contended is
	// true ("elapsed 12m0s past p99 8m30s; host saturated 62% of the run").
	ContentionReason string `json:"contention_reason,omitempty"`
	// SaturatedShare is the fraction (0..1) of this holder's observed host
	// samples during which the host was saturated by external load. It
	// backs the end-of-run attribution regardless of the contended verdict;
	// zero for reattached holders whose accounting did not survive a restart.
	SaturatedShare float64 `json:"saturated_share,omitempty"`
	// Origin names who dispatched this holder's run -- local work or
	// controller-dispatched work on a registered runner. Empty for local
	// runs and for leases whose origin did not survive a daemon restart.
	Origin Origin `json:"origin,omitempty"`
}

// Waiter is one run queued for admission, as reported in a
// [QueueState]. Waiters appear in admission order; Position is its
// 1-based place in that order.
type Waiter struct {
	RunID string `json:"run_id"`
	// ParticipantID is the daemon lease key when it differs from RunID.
	ParticipantID string `json:"participant_id,omitempty"`
	// DisplayRunID is the label queue views print for this row. Empty
	// means display RunID.
	DisplayRunID string `json:"display_run_id,omitempty"`
	// Pipeline is the pipeline name behind the run, for display. Empty
	// when the run did not report one.
	Pipeline string `json:"pipeline,omitempty"`
	// Repo is the short repo name the run was launched from, for
	// display. Empty when the run did not report one.
	Repo string `json:"repo,omitempty"`
	// Position is the waiter's 1-based place in admission order; 1 is
	// admitted next.
	Position   int           `json:"position"`
	Priority   int           `json:"priority,omitempty"`
	Resources  HostResources `json:"resources"`
	Semaphores []string      `json:"semaphores,omitempty"`
	// WaitingOn names the resources the waiter lacks room for right now
	// -- host dimensions ("cores", "memory") and full semaphore keys.
	// Empty means the waiter is held only by admission order behind a
	// heavier request ahead of it.
	WaitingOn []string `json:"waiting_on,omitempty"`
	// BlockingReason is a one-line, human explanation of why this waiter
	// is not yet admitted, naming what it needs against what is available
	// and any external load ("needs 5.0 cores; 4.8 available (external
	// load 3.2)"). Empty when the wait is pure admission-order queueing or
	// the daemon predates this field.
	BlockingReason string `json:"blocking_reason,omitempty"`
	// WaitingMS is how long the run has been queued, in milliseconds.
	WaitingMS int64 `json:"waiting_ms,omitempty"`
	// CostSource names how Resources was resolved ("pin", "measured",
	// "default").
	CostSource string `json:"cost_source,omitempty"`
	// CostRationale is the short human phrase explaining that CostSource
	// ("measured p95 over 12 runs", "first run, conservative default until
	// measured"), also folded into BlockingReason. Empty when the source is
	// unknown.
	CostRationale string `json:"cost_rationale,omitempty"`
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
	// Origin names who dispatched this waiter's run -- local work or
	// controller-dispatched work on a registered runner. Empty is local.
	Origin Origin `json:"origin,omitempty"`
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
	// Events summarizes the daemon's recent admission outcomes. Nil for
	// older daemons that do not keep the window.
	Events *EventsWindow `json:"events,omitempty"`
	// Budget describes the machine budget capping the ledger below the
	// host total, so the queue view can show the constraint. Nil when no
	// budget is set (the full machine is available).
	Budget *BudgetState `json:"budget,omitempty"`
	// Container reports the cgroup limit clamping capacity below the host,
	// so a daemon in a resource-limited container shows the true ceiling.
	// Nil when no container limit binds (the host total stands).
	Container *ContainerLimit `json:"container,omitempty"`
	// IgnoreExternal reports that the operator has told admission to ignore
	// measured non-sparkwing load: the External column still shows the real
	// reading, but admission does not subtract it. False for older daemons
	// and the default configuration.
	IgnoreExternal bool `json:"ignore_external,omitempty"`
	// CapacityChange records the most recent time the daemon re-derived a
	// different machine capacity while running (a hot VM resize or a cgroup
	// quota edit), for the queue header. Nil when capacity has held steady
	// since start, or for older daemons.
	CapacityChange *CapacityChange `json:"capacity_change,omitempty"`
	// Runners carries each registered runner's advertised free capacity when
	// the state comes from a controller's unified admission view. Empty for
	// the local daemon, which arbitrates only its own host.
	Runners []RunnerHeadroom `json:"runners,omitempty"`
}

// RunnerHeadroom is one registered runner's most recently advertised free
// capacity, as surfaced in a controller's unified queue view: the local
// admission daemon's grantable cores and memory after the operator reserve,
// plus its queue depth. It is soft, staleable observability, never a gate.
type RunnerHeadroom struct {
	Name        string  `json:"name"`
	Cores       float64 `json:"cores"`
	MemoryBytes int64   `json:"memory_bytes,omitempty"`
	QueueDepth  int     `json:"queue_depth,omitempty"`
}

// CapacityChange reports a runtime shift in the machine capacity the ledger
// admits into: the daemon re-derives capacity periodically, so a resize or
// quota edit is picked up without a restart. FromCores and ToCores are the
// budgeted core totals before and after the shift.
type CapacityChange struct {
	FromCores float64 `json:"from_cores"`
	ToCores   float64 `json:"to_cores"`
	// AtMS is when the change was applied, in Unix milliseconds.
	AtMS int64 `json:"at_ms,omitempty"`
}

// ContainerLimit reports the cgroup capacity ceiling a daemon runs under
// when it is smaller than the host: inside a 6 GiB container on a 24 GiB
// host, capacity is the container's, not the machine's. Each dimension is
// present only when the container constrains it below the host.
type ContainerLimit struct {
	// Cores is the container's core ceiling, zero when CPU is unconstrained.
	Cores float64 `json:"cores,omitempty"`
	// MemoryBytes is the container's memory ceiling, zero when memory is
	// unconstrained.
	MemoryBytes int64 `json:"memory_bytes,omitempty"`
	// HostCores is the host's full core count, for showing the container
	// ceiling against the machine it sits on.
	HostCores float64 `json:"host_cores,omitempty"`
	// HostMemoryBytes is the host's full memory.
	HostMemoryBytes int64 `json:"host_memory_bytes,omitempty"`
}

// BudgetState reports the machine budget behind a [QueueState]: the
// capped host capacity the ledger admits into, against the machine total
// it was measured from. It is present only when a budget is configured.
type BudgetState struct {
	// Cores is the budgeted core cap the ledger admits into.
	Cores float64 `json:"cores"`
	// MachineCores is the host's full measured core count.
	MachineCores float64 `json:"machine_cores"`
	// MemoryBytes is the budgeted memory cap the ledger admits into.
	MemoryBytes int64 `json:"memory_bytes"`
	// MachineMemoryBytes is the host's full measured memory.
	MachineMemoryBytes int64 `json:"machine_memory_bytes"`
	// Enforce reports whether the budget is hardened at the OS level
	// (a cgroup on Linux, background scheduling on macOS) in addition to
	// capping admission.
	Enforce bool `json:"enforce,omitempty"`
}

// EventsWindow summarizes the daemon's rolling window of admission
// outcomes -- the data behind the queue view's one-line health summary.
// Counts cover the window's span ending now; zero-valued categories
// simply did not occur.
type EventsWindow struct {
	// WindowMS is the span the window covers, in milliseconds.
	WindowMS int64 `json:"window_ms"`
	// Runs is how many admissions were granted in the window.
	Runs int `json:"runs"`
	// MedianWaitMS is the median submit-to-grant wait across those
	// grants, in milliseconds.
	MedianWaitMS int64 `json:"median_wait_ms"`
	// Evictions counts holders superseded under cancel_others, per
	// contested key.
	Evictions []EvictionCount `json:"evictions,omitempty"`
	// QueueTimeouts is how many waiters abandoned the queue when a
	// bounded OnLimit:Queue wait elapsed.
	QueueTimeouts int `json:"queue_timeouts,omitempty"`
	// Cancellations is how many queued or running admissions were
	// cancelled (operator cancel, interrupt, or a waiter's process
	// going away).
	Cancellations int `json:"cancellations,omitempty"`
	// Contended is how many runs the daemon flagged as throttled by host
	// contention while they held admission in the window.
	Contended int `json:"contended,omitempty"`
	// Rejections counts requests the daemon refused as malformed, per
	// cause, so a repeated invalid-request pattern is visible to the queue
	// view and doctor. Empty when no request was rejected and for older
	// daemons.
	Rejections []RejectionCount `json:"rejections,omitempty"`
}

// EvictionCount is one contested key's eviction tally in an
// [EventsWindow].
type EvictionCount struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// RejectionCount is one malformed-request cause's tally in an
// [EventsWindow]. Cause is a short stable label ("cost_source", "request")
// the queue view and doctor aggregate on.
type RejectionCount struct {
	Cause string `json:"cause"`
	Count int    `json:"count"`
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

// StatsReset asks the daemon to clear its rolling admission-outcome window
// (the data behind the queue view's recent-events summary), on a dedicated
// control connection. The daemon answers with [StatsResetAck].
type StatsReset struct{}

// StatsResetAck confirms the daemon cleared its admission-outcome window.
type StatsResetAck struct{}

func (*StatsReset) wireType() MessageType       { return TypeStatsReset }
func (*StatsResetAck) wireType() MessageType    { return TypeStatsResetAck }
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
