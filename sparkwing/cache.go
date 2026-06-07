package sparkwing

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// OnLimitPolicy enumerates the behaviors of a new arrival when the
// Max concurrent limit for a Cache namespace is already full. Policy
// is a property of the arrival, not the namespace, so different nodes
// using the same Namespace may declare different OnLimit values and
// each gets its own behavior.
type OnLimitPolicy string

const (
	// Queue: new arrival waits in FIFO order for a slot to open.
	// Existing holder(s) keep running. Default when Namespace is set.
	Queue OnLimitPolicy = "queue"

	// Coalesce: new arrival subscribes to the current holder's
	// (leader's) in-flight execution. When the leader completes,
	// followers inherit the leader's output and terminal outcome.
	// Follower's log stream points at leader's log stream.
	//
	// Coalesce does NOT memoize beyond the in-flight window; late
	// arrivals after leader completion run fresh. Add ContentHash to
	// enable memoization across time.
	//
	// Job-level only; rejected on Plan.Cache().
	Coalesce OnLimitPolicy = "coalesce"

	// Skip: new arrival succeeds immediately as a no-op without
	// running. Terminal outcome is "skipped-concurrent" (distinct
	// from SkipIf's "skipped" so dashboards can surface the cause).
	Skip OnLimitPolicy = "skip"

	// Fail: new arrival returns a rejection error. Existing holder
	// keeps running. Terminal outcome is "failed".
	Fail OnLimitPolicy = "fail"

	// CancelOthers: new arrival cancels existing holder(s) oldest-first
	// until the slot frees, then runs. Cancelled holders' terminal
	// outcome is "superseded" (distinct from "cancelled" so dashboards
	// can surface "evicted by newer run" vs "operator cancelled").
	//
	// BEST-EFFORT. Side effects executed before the cancel signal
	// arrived (docker push, gitops commit, S3 sync, webhook dispatch)
	// persist. Cancellation stops further progress; it does not roll
	// back.
	CancelOthers OnLimitPolicy = "cancel_others"
)

// DefaultCacheTTL is the TTL applied when ContentHash is set but
// CacheTTL is not.
const DefaultCacheTTL = 7 * 24 * time.Hour

// MaxCacheTTL is the ceiling; CacheOptions.CacheTTL values above
// this are clamped with a plan-time warning log. Lets operators
// pick their own retention without enabling unbounded cache growth.
const MaxCacheTTL = 35 * 24 * time.Hour

// CacheOptions configures the deduplication / coordination behavior
// of a Job or Plan. A node/plan with no Cache() call has no
// coordination. Setting Namespace enables coordination; other fields
// are optional and fall back to sensible defaults.
type CacheOptions struct {
	// Namespace is the global-within-controller coordination
	// identifier. Required to enable coordination. Empty = no
	// coordination (equivalent to not calling Cache at all).
	Namespace string

	// Max is the maximum concurrent holders of this namespace. Zero
	// or unset = 1 (mutex). Values > 1 are semaphores. Only
	// meaningful when Namespace is set.
	//
	// Different callers declaring different Max values on the same
	// namespace is tolerated: the controller applies latest-wins and
	// emits a drift warning on the run-scoped event stream so the
	// change is discoverable without surprise.
	Max int

	// OnLimit is the policy for new arrivals when the slot is full.
	// Unset = Queue.
	OnLimit OnLimitPolicy

	// ContentHash, when set, memoizes the node's output after
	// successful completion. Future arrivals whose ContentHash(ctx)
	// result matches a stored entry skip execution and replay the
	// stored output. Typically composed with OnLimit: Coalesce to
	// prevent thundering herd on the initial miss. Only meaningful
	// when Namespace is set.
	//
	// A hit replays the typed output and nothing else: the action,
	// steps, and the node's Verify postcondition are all skipped, and
	// filesystem side-effects are NOT reproduced. A node whose real
	// product is files it wrote to disk will hit, return its output,
	// and leave the run green with those files absent -- only memoize
	// nodes whose value is fully captured by their returned output.
	//
	// The restore is cross-run: a hit from a previous run writes the
	// output onto the current run's node row, so a downstream RefTo[T]
	// resolves it, not only in-flight Coalesce followers.
	//
	// Return [NoCache] to explicitly opt this invocation out of
	// memoization (distinct from returning the zero CacheKey, which
	// is treated as a missing-key warning).
	ContentHash CacheKeyFn

	// CacheTTL bounds how long a memoized output remains reusable.
	// Defaults to DefaultCacheTTL (7 days) when ContentHash is set
	// but CacheTTL is zero. Values above MaxCacheTTL (35 days) are
	// clamped at call time with a warning log.
	CacheTTL time.Duration

	// CancelTimeout applies only to OnLimit: CancelOthers. Bounds
	// how long the new arrival waits for evicted holders to reach
	// terminal state before the controller force-releases the slot.
	// Default 60s.
	CancelTimeout time.Duration

	// QueueTimeout applies only to OnLimit: Queue. Bounds how long a
	// queued arrival waits for a slot before giving up. Zero (the
	// default) means wait indefinitely -- the historical behavior. When
	// set, a waiter that hasn't been promoted within the duration fails
	// cleanly with failure_reason "queue_timeout" instead of blocking
	// the run forever. This is the knob a gate-shaped pipeline shared
	// across processes wants: serialize on the namespace, but don't hang
	// a contending run indefinitely. Only meaningful with a set
	// Namespace and OnLimit: Queue.
	QueueTimeout time.Duration
}

// HasNamespace reports whether these options declare coordination.
func (o CacheOptions) HasNamespace() bool { return o.Namespace != "" }

// rejectTypoShape catches the common typo: a CacheOptions literal
// with non-zero Max / OnLimit / ContentHash / CacheTTL / CancelTimeout
// but Namespace left empty. Without this check the whole struct
// silently no-ops -- the author wanted coordination + memoization,
// the SDK gave them nothing. Bare CacheOptions{} (every field zero)
// stays a legal no-op.
func (o CacheOptions) rejectTypoShape(ctx string) {
	if o.Namespace != "" {
		return
	}
	var set []string
	if o.Max != 0 {
		set = append(set, "Max")
	}
	if o.OnLimit != "" {
		set = append(set, "OnLimit")
	}
	if o.ContentHash != nil {
		set = append(set, "ContentHash")
	}
	if o.CacheTTL != 0 {
		set = append(set, "CacheTTL")
	}
	if o.CancelTimeout != 0 {
		set = append(set, "CancelTimeout")
	}
	if o.QueueTimeout != 0 {
		set = append(set, "QueueTimeout")
	}
	if len(set) == 0 {
		return
	}
	panic(fmt.Sprintf(
		"sparkwing: Cache on %s: CacheOptions has %v set but Namespace is empty -- "+
			"either set Namespace to enable coordination, or pass a bare CacheOptions{} to disable",
		ctx, set,
	))
}

func (o *CacheOptions) validate(ctx string, isPlan bool) {
	if o.Max < 0 {
		panic(fmt.Sprintf("sparkwing: Cache on %s: Max must be >= 0, got %d", ctx, o.Max))
	}
	if o.CacheTTL < 0 {
		panic(fmt.Sprintf("sparkwing: Cache on %s: CacheTTL must be >= 0, got %s", ctx, o.CacheTTL))
	}
	if o.QueueTimeout < 0 {
		panic(fmt.Sprintf("sparkwing: Cache on %s: QueueTimeout must be >= 0, got %s", ctx, o.QueueTimeout))
	}
	if isPlan && o.OnLimit == Coalesce {
		panic("sparkwing: Cache on plan: OnLimit:Coalesce is node-only (coalescing whole runs is meaningless; scope .Cache() to a gate node instead)")
	}
	if isPlan && o.ContentHash != nil {
		panic("sparkwing: Cache on plan: ContentHash is node-only (plans have side effects not captured in a single output; attach ContentHash to a specific gate node instead)")
	}
	if o.OnLimit == "" {
		o.OnLimit = Queue
	}
	if o.Max == 0 {
		o.Max = 1
	}
	if o.ContentHash != nil && o.CacheTTL == 0 {
		o.CacheTTL = DefaultCacheTTL
	}
	if o.CacheTTL > MaxCacheTTL {
		slog.Warn("sparkwing.Cache: CacheTTL exceeds MaxCacheTTL; clamping",
			"context", ctx, "requested", o.CacheTTL, "max", MaxCacheTTL)
		o.CacheTTL = MaxCacheTTL
	}
}

// Cache applies the given options to this node's coordination and
// memoization. An empty CacheOptions{} is a no-op; pass Namespace to
// enable coordination. Repeated calls overwrite.
//
// Examples:
//
//	// Mutex: only one at a time, queue the rest.
//	plan.Add("deploy", &Deploy{}).Cache(sparkwing.CacheOptions{Namespace: "deploy-prod"})
//
//	// Semaphore: up to 3 concurrent, queue the rest.
//	plan.Add("index", &Index{}).Cache(sparkwing.CacheOptions{
//	    Namespace: "es-writer", Max: 3,
//	})
//
//	// Content-addressed single-flight: memoize the output, coalesce
//	// on initial miss so a burst of identical triggers collapses to
//	// one execution.
//	plan.Add("image", &Build{}).Cache(sparkwing.CacheOptions{
//	    Namespace:   "build",
//	    OnLimit:     sparkwing.Coalesce,
//	    ContentHash: func(ctx context.Context) sparkwing.CacheKey {
//	        return sparkwing.Key("image", repo.Ref().Get(ctx).SHA)
//	    },
//	    CacheTTL: 24 * time.Hour,
//	})
func (n *JobNode) Cache(opts CacheOptions) *JobNode {
	if !opts.HasNamespace() {
		opts.rejectTypoShape("node " + n.id)
		n.cache = CacheOptions{}
		return n
	}
	opts.validate("node "+n.id, false)
	n.cache = opts
	return n
}

// CacheOpts returns the node's currently-set CacheOptions, or the
// zero value when .Cache() was not called.
func (n *JobNode) CacheOpts() CacheOptions { return n.cache }

// Cache applies the given options to the entire plan. The lock is
// acquired before any node dispatches and released when the plan
// reaches a terminal status (success, failed, cancelled, superseded).
//
// OnLimit: Coalesce is rejected because coalescing whole runs is
// meaningless: plans have side effects not captured in a single
// output. Scope .Cache() to a specific gate node for that behavior.
//
// Empty CacheOptions{} is a no-op; pass Namespace to enable
// coordination.
func (p *Plan) Cache(opts CacheOptions) *Plan {
	if !opts.HasNamespace() {
		opts.rejectTypoShape("plan")
		p.cache = CacheOptions{}
		return p
	}
	opts.validate("plan", true)
	p.cache = opts
	return p
}

// CacheOpts returns the plan-level CacheOptions, or the zero value
// when none was set.
func (p *Plan) CacheOpts() CacheOptions { return p.cache }

var _ = context.Background
