package objectguard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// CeilingReason names the ceiling a freeze came from.
type CeilingReason string

const (
	// CeilingBytes is the stored-byte ceiling.
	CeilingBytes CeilingReason = "bytes"
	// CeilingObjects is the object-count ceiling.
	CeilingObjects CeilingReason = "objects"
)

// ErrCeilingFrozen matches every *CeilingError under errors.Is.
var ErrCeilingFrozen = errors.New("object-store bucket ceiling reached")

// CeilingError reports a write refused because the bucket sits above
// its ceiling. It names the measurement and the ceiling it crossed, so
// an operator can act on the message alone.
type CeilingError struct {
	Reason CeilingReason
	// Limit is the ceiling that froze writes, in bytes or objects.
	Limit int64
	// Observed is the bucket total the freeze was taken on.
	Observed int64
	// Frozen is when the bucket entered its frozen state.
	Frozen time.Time
}

func (e *CeilingError) Error() string {
	return fmt.Sprintf(
		"object-store bucket ceiling reached: the bucket holds %d %s against a ceiling of %d, so writes froze at %s and further object writes are refused. "+
			"Delete objects until the next reconciliation measures the bucket under the ceiling, raise %s, or clear the freeze with "+
			"`sparkwing cluster object-store reset-breaker --profile NAME`",
		e.Observed, e.Reason, e.Limit, e.Frozen.UTC().Format(time.RFC3339), ceilingEnv(e.Reason),
	)
}

func (e *CeilingError) Unwrap() error { return ErrCeilingFrozen }

// RetryableError reports the refusal as final. A bucket over its
// ceiling is not under it again by the next attempt.
func (e *CeilingError) RetryableError() bool { return false }

// CeilingLimit is the pair of ceilings and the pair of warning marks a
// bucket is held to. A zero or negative field removes that bound, which
// is the shipped default: a bucket is unlimited until an operator says
// otherwise.
type CeilingLimit struct {
	MaxBytes    int64
	MaxObjects  int64
	WarnBytes   int64
	WarnObjects int64
}

// Enforced reports whether either ceiling can freeze writes.
func (l CeilingLimit) Enforced() bool { return l.MaxBytes > 0 || l.MaxObjects > 0 }

// DefaultCeilingReconcile is how often a measured bucket total replaces
// the incremental counters when an operator sets no interval.
const DefaultCeilingReconcile = time.Hour

// CeilingConfig is a Ceiling's whole configuration.
type CeilingConfig struct {
	Limit CeilingLimit
	// Reconcile is the gap between measured bucket totals. Zero uses
	// DefaultCeilingReconcile; negative turns reconciliation off and
	// leaves the incremental counters as the only measurement.
	Reconcile time.Duration
}

// Usage is a bucket total: the bytes it stores and the objects it
// holds, as of ObservedAt.
type Usage struct {
	Bytes      int64     `json:"bytes"`
	Objects    int64     `json:"objects"`
	ObservedAt time.Time `json:"observed_at,omitzero"`
}

// CeilingState is the ceiling at a point in time, as the health route
// and the metrics collector report it.
type CeilingState struct {
	// Enforced is false while neither ceiling is set, which is the
	// shipped default and means the ceiling never refuses a write.
	Enforced     bool          `json:"enforced"`
	MaxBytes     int64         `json:"max_bytes"`
	MaxObjects   int64         `json:"max_objects"`
	WarnBytes    int64         `json:"warn_bytes"`
	WarnObjects  int64         `json:"warn_objects"`
	Bytes        int64         `json:"bytes"`
	Objects      int64         `json:"objects"`
	MeasuredAt   time.Time     `json:"measured_at,omitzero"`
	ReconciledAt time.Time     `json:"reconciled_at,omitzero"`
	Reconcile    string        `json:"reconcile_interval,omitempty"`
	Warning      bool          `json:"warning"`
	Frozen       bool          `json:"frozen"`
	FrozenAt     time.Time     `json:"frozen_at,omitzero"`
	FrozenReason CeilingReason `json:"frozen_reason,omitempty"`
	Freezes      uint64        `json:"freezes_total"`
	Refused      uint64        `json:"refused_total"`
}

// Ceiling holds a bucket to a total size and a total object count.
//
// Counting is incremental: [Ceiling.Record] adds one write's bytes as
// it happens, which costs nothing per request. Those counters drift,
// because an overwrite counts its key twice and a delete cannot know
// what it removed, so [Ceiling.Observe] replaces them with a measured
// total on the reconciliation interval.
//
// A total at or above a ceiling freezes writes: every further object
// write is refused with a *CeilingError until a measurement puts the
// bucket back under the ceiling or an operator thaws it. Safe for
// concurrent use.
type Ceiling struct {
	mu        sync.Mutex
	limit     CeilingLimit
	reconcile time.Duration

	bytes      int64
	objects    int64
	measured   time.Time
	reconciled time.Time

	frozen       bool
	frozenAt     time.Time
	frozenReason CeilingReason
	frozenOn     int64
	freezes      uint64
	refused      uint64

	now func() time.Time
}

// NewCeiling builds a Ceiling from cfg. A cfg with no ceiling set never
// refuses a write.
func NewCeiling(cfg CeilingConfig) *Ceiling {
	c := &Ceiling{limit: cfg.Limit, reconcile: cfg.Reconcile, now: time.Now}
	if c.reconcile == 0 {
		c.reconcile = DefaultCeilingReconcile
	}
	return c
}

// Configure replaces the ceilings and the reconciliation interval on a
// running Ceiling and re-evaluates the freeze against the counters it
// already holds, so raising a ceiling above the current total thaws the
// bucket at once. The process that owns the limiter calls it to apply
// its own configuration; a raise made this way lasts as long as the
// process, so an operator raises the environment alongside it.
func (c *Ceiling) Configure(cfg CeilingConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limit = cfg.Limit
	c.reconcile = cfg.Reconcile
	if c.reconcile == 0 {
		c.reconcile = DefaultCeilingReconcile
	}
	c.evaluateLocked()
}

// Enforced reports whether either ceiling is set.
func (c *Ceiling) Enforced() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.limit.Enforced()
}

// Reconcile is the gap between measured bucket totals. A non-positive
// value means the caller should not measure at all.
func (c *Ceiling) Reconcile() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reconcile
}

// Record adds one write's delta to the incremental counters and
// re-evaluates the freeze. Bytes and objects may be negative, which is
// how a deletion gives its object back.
func (c *Ceiling) Record(bytes, objects int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bytes += bytes
	c.objects += objects
	if c.bytes < 0 {
		c.bytes = 0
	}
	if c.objects < 0 {
		c.objects = 0
	}
	c.measured = c.now().UTC()
	c.evaluateLocked()
}

// Observe replaces the counters with a measured bucket total and
// re-evaluates the freeze. A measurement under the ceiling thaws a
// frozen bucket, which is how deleting objects brings writes back.
func (c *Ceiling) Observe(u Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bytes = u.Bytes
	c.objects = u.Objects
	at := u.ObservedAt
	if at.IsZero() {
		at = c.now()
	}
	c.measured = at.UTC()
	c.reconciled = c.measured
	c.evaluateLocked()
}

// safety: the freeze is taken from whichever ceiling the totals crossed first,
// so the error names the ceiling an operator has to act on.
func (c *Ceiling) evaluateLocked() {
	reason, observed, over := c.overLocked()
	if !over {
		c.frozen = false
		c.frozenReason = ""
		c.frozenAt = time.Time{}
		c.frozenOn = 0
		return
	}
	if c.frozen && c.frozenReason == reason {
		c.frozenOn = observed
		return
	}
	if !c.frozen {
		c.freezes++
		c.frozenAt = c.now().UTC()
	}
	c.frozen = true
	c.frozenReason = reason
	c.frozenOn = observed
}

func (c *Ceiling) overLocked() (CeilingReason, int64, bool) {
	if c.limit.MaxBytes > 0 && c.bytes >= c.limit.MaxBytes {
		return CeilingBytes, c.bytes, true
	}
	if c.limit.MaxObjects > 0 && c.objects >= c.limit.MaxObjects {
		return CeilingObjects, c.objects, true
	}
	return "", 0, false
}

func (c *Ceiling) warningLocked() bool {
	if c.limit.WarnBytes > 0 && c.bytes >= c.limit.WarnBytes {
		return true
	}
	return c.limit.WarnObjects > 0 && c.objects >= c.limit.WarnObjects
}

// Allow reports whether an object write may proceed. A frozen bucket
// returns a *CeilingError and counts the refusal.
func (c *Ceiling) Allow() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.frozen {
		return nil
	}
	c.refused++
	limit := c.limit.MaxBytes
	if c.frozenReason == CeilingObjects {
		limit = c.limit.MaxObjects
	}
	return &CeilingError{Reason: c.frozenReason, Limit: limit, Observed: c.frozenOn, Frozen: c.frozenAt}
}

// Frozen reports whether the ceiling is refusing writes.
func (c *Ceiling) Frozen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frozen
}

// Thaw clears a freeze and reports whether one was in place. The next
// measurement freezes the bucket again while it stays over the ceiling,
// so a thaw buys the window an operator needs to delete objects or
// raise the limit.
func (c *Ceiling) Thaw() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	was := c.frozen
	c.frozen = false
	c.frozenReason = ""
	c.frozenAt = time.Time{}
	c.frozenOn = 0
	return was
}

// State snapshots the ceiling for the health route and metrics.
func (c *Ceiling) State() CeilingState {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := CeilingState{
		Enforced:     c.limit.Enforced(),
		MaxBytes:     c.limit.MaxBytes,
		MaxObjects:   c.limit.MaxObjects,
		WarnBytes:    c.limit.WarnBytes,
		WarnObjects:  c.limit.WarnObjects,
		Bytes:        c.bytes,
		Objects:      c.objects,
		MeasuredAt:   c.measured,
		ReconciledAt: c.reconciled,
		Warning:      c.warningLocked(),
		Frozen:       c.frozen,
		FrozenAt:     c.frozenAt,
		FrozenReason: c.frozenReason,
		Freezes:      c.freezes,
		Refused:      c.refused,
	}
	if c.reconcile > 0 {
		st.Reconcile = c.reconcile.String()
	}
	return st
}

// UsageSource measures a whole bucket. The Ceiling calls one on the
// reconciliation interval and never per request, because a listing is
// billed per thousand objects.
type UsageSource func(context.Context) (Usage, error)

// ReconcileWith measures the bucket through src and folds the result
// into the ceiling. It is a no-op when no ceiling is set, so an
// unlimited install never pays for a listing.
func (c *Ceiling) ReconcileWith(ctx context.Context, src UsageSource) error {
	if src == nil || !c.Enforced() {
		return nil
	}
	u, err := src(ctx)
	if err != nil {
		return fmt.Errorf("measure bucket usage: %w", err)
	}
	c.Observe(u)
	return nil
}

func ceilingEnv(r CeilingReason) string {
	if r == CeilingObjects {
		return EnvMaxBucketObjects
	}
	return EnvMaxBucketBytes
}
