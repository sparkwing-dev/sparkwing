package sparkwing

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// OnLimitPolicy enumerates the behaviors of a new arrival when the
// Max concurrent limit for a Cache key is already full. Policy is
// a property of the arrival, not the key, so different nodes using
// the same Key may declare different OnLimit values and each gets
// its own behavior.
type OnLimitPolicy string

const (
	// Queue: new arrival waits in FIFO order for a slot to open.
	// Existing holder(s) keep running. Default when Key is set.
	Queue OnLimitPolicy = "queue"

	// Coalesce: new arrival subscribes to the current holder's
	// (leader's) in-flight execution. When the leader completes,
	// followers inherit the leader's output and terminal outcome.
	// Follower's log stream points at leader's log stream.
	//
	// Coalesce does NOT memoize beyond the in-flight window; late
	// arrivals after leader completion run fresh. Add CacheKey to
	// enable memoization across time.
	//
	// Node-level only; rejected on Plan.Cache().
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

// DefaultCacheTTL is the TTL applied when CacheKey is set but
// CacheTTL is not.
const DefaultCacheTTL = 7 * 24 * time.Hour

// MaxCacheTTL is the ceiling; CacheOptions.CacheTTL values above
// this are clamped with a plan-time warning log. Lets operators
// pick their own retention without enabling unbounded cache growth.
const MaxCacheTTL = 35 * 24 * time.Hour

// CacheOptions configures the deduplication / coordination behavior
// of a Node or Plan. A node/plan with no Cache() call has no
// coordination. Setting Key enables coordination; other fields are
// optional and fall back to sensible defaults.
type CacheOptions struct {
	// Key is the global-within-controller coordination identifier.
	// Required to enable coordination. Empty = no coordination
	// (equivalent to not calling Cache at all).
	Key string

	// Max is the maximum concurrent holders of this key. Zero or
	// unset = 1 (mutex). Values > 1 are semaphores. Only meaningful
	// when Key is set.
	//
	// Different callers declaring different Max values on the same
	// key is tolerated: the controller applies latest-wins and emits
	// a drift warning on the run-scoped event stream so the change
	// is discoverable without surprise.
	Max int

	// OnLimit is the policy for new arrivals when the slot is full.
	// Unset = Queue.
	OnLimit OnLimitPolicy

	// CacheKey, when set, memoizes the node's output after successful
	// completion. Future arrivals whose CacheKey(ctx) result matches
	// a stored entry skip execution and replay the stored output.
	// Typically composed with OnLimit: Coalesce to prevent thundering
	// herd on the initial miss. Only meaningful when Key is set.
	CacheKey CacheKeyFn

	// CacheTTL bounds how long a memoized output remains reusable.
	// Defaults to DefaultCacheTTL (7 days) when CacheKey is set but
	// CacheTTL is zero. Values above MaxCacheTTL (35 days) are
	// clamped at call time with a warning log.
	CacheTTL time.Duration

	// CancelTimeout applies only to OnLimit: CancelOthers. Bounds
	// how long the new arrival waits for evicted holders to reach
	// terminal state before the controller force-releases the slot.
	// Default 60s.
	CancelTimeout time.Duration
}

// HasKey reports whether these options declare coordination.
func (o CacheOptions) HasKey() bool { return o.Key != "" }

func (o *CacheOptions) validate(ctx string, isPlan bool) {
	if o.Max < 0 {
		panic(fmt.Sprintf("sparkwing: Cache on %s: Max must be >= 0, got %d", ctx, o.Max))
	}
	if o.CacheTTL < 0 {
		panic(fmt.Sprintf("sparkwing: Cache on %s: CacheTTL must be >= 0, got %s", ctx, o.CacheTTL))
	}
	if isPlan && o.OnLimit == Coalesce {
		panic("sparkwing: Cache on plan: OnLimit:Coalesce is node-only (coalescing whole runs is meaningless; scope .Cache() to a gate node instead)")
	}
	if isPlan && o.CacheKey != nil {
		panic("sparkwing: Cache on plan: CacheKey is node-only (plans have side effects not captured in a single output; attach CacheKey to a specific gate node instead)")
	}
	if o.OnLimit == "" {
		o.OnLimit = Queue
	}
	if o.Max == 0 {
		o.Max = 1
	}
	if o.CacheKey != nil && o.CacheTTL == 0 {
		o.CacheTTL = DefaultCacheTTL
	}
	if o.CacheTTL > MaxCacheTTL {
		slog.Warn("sparkwing.Cache: CacheTTL exceeds MaxCacheTTL; clamping",
			"context", ctx, "requested", o.CacheTTL, "max", MaxCacheTTL)
		o.CacheTTL = MaxCacheTTL
	}
}

// Cache applies the given options to this node's coordination and
// memoization. An empty CacheOptions{} is a no-op; pass Key to enable
// coordination. Repeated calls overwrite.
//
// Examples:
//
//	// Mutex: only one at a time, queue the rest.
//	plan.Add("deploy", &Deploy{}).Cache(sparkwing.CacheOptions{Key: "deploy-prod"})
//
//	// Semaphore: up to 3 concurrent, queue the rest.
//	plan.Add("index", &Index{}).Cache(sparkwing.CacheOptions{
//	    Key: "es-writer", Max: 3,
//	})
//
//	// Cache-keyed single-flight: memoize the output, coalesce on
//	// initial miss so a burst of identical triggers collapses to
//	// one execution.
//	plan.Add("image", &Build{}).Cache(sparkwing.CacheOptions{
//	    Key:      "build",
//	    OnLimit:  sparkwing.Coalesce,
//	    CacheKey: func(ctx context.Context) sparkwing.CacheKey {
//	        return sparkwing.Key("image", repo.Ref().Get(ctx).SHA)
//	    },
//	    CacheTTL: 24 * time.Hour,
//	})
func (n *Node) Cache(opts CacheOptions) *Node {
	if !opts.HasKey() {
		n.cache = CacheOptions{}
		return n
	}
	opts.validate("node "+n.id, false)
	n.cache = opts
	return n
}

// CacheOpts returns the node's currently-set CacheOptions, or the
// zero value when .Cache() was not called.
func (n *Node) CacheOpts() CacheOptions { return n.cache }

// Cache applies the given options to the entire plan. The lock is
// acquired before any node dispatches and released when the plan
// reaches a terminal status (success, failed, cancelled, superseded).
//
// OnLimit: Coalesce is rejected because coalescing whole runs is
// meaningless: plans have side effects not captured in a single
// output. Scope .Cache() to a specific gate node for that behavior.
//
// Empty CacheOptions{} is a no-op; pass Key to enable coordination.
func (p *Plan) Cache(opts CacheOptions) *Plan {
	if !opts.HasKey() {
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
