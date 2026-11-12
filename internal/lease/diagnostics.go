package lease

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

// Identity binds diagnostics to a host-owned unit, never its credential.
type Identity struct {
	Unit       string `json:"unit,omitempty"`
	Role       string `json:"role"`
	Run        string `json:"run"`
	Node       string `json:"node,omitempty"`
	Generation int64  `json:"generation,omitempty"`
}

// Event reports observed keeper state; missing sequences or end events mean incomplete evidence.
type Event struct {
	BootID            string    `json:"boot_id,omitempty"`
	Platform          string    `json:"platform"`
	ProcessBirth      string    `json:"process_birth,omitempty"`
	IdentityComplete  bool      `json:"identity_complete"`
	ClockOrigin       time.Time `json:"clock_origin"`
	Identity          Identity  `json:"identity"`
	Instance          string    `json:"instance"`
	PID               int       `json:"pid"`
	Sequence          uint64    `json:"sequence"`
	Dropped           uint64    `json:"dropped"`
	Kind              string    `json:"kind"`
	MonoBefore        int64     `json:"mono_before_ns"`
	Wall              time.Time `json:"wall"`
	MonoAfter         int64     `json:"mono_after_ns"`
	Sent              time.Time `json:"sent,omitzero"`
	ExpiresAt         time.Time `json:"expires_at,omitzero"`
	Deadline          time.Time `json:"deadline,omitzero"`
	ExecutionDeadline time.Time `json:"execution_deadline,omitzero"`
	Renewal           uint64    `json:"renewal"`
	InFlight          bool      `json:"renewal_in_flight"`
	Outcome           string    `json:"outcome,omitempty"`
}

type diagnosticRecord struct {
	event Event
	trace *keeperTrace
}

// Diagnostics isolates best-effort delivery from renewal and expiry. Close cancels delivery;
// a sink must honor its context. A blocked or failing sink cannot block a keeper.
type Diagnostics struct {
	queue     chan diagnosticRecord
	ctx       context.Context
	cancel    context.CancelFunc
	origin    time.Time
	instances atomic.Uint64
	birth     string
	boot      string
}

// NewDiagnostics starts one bounded delivery worker. The sink receives no execution authority.
func NewDiagnostics(parent context.Context, sink func(context.Context, Event) error) *Diagnostics {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	d := &Diagnostics{queue: make(chan diagnosticRecord, 256), ctx: ctx, cancel: cancel, origin: time.Now()}
	birth, birthErr := procgroup.ProcessBirth(os.Getpid())
	if birthErr == nil {
		d.birth = birth
	}
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		if err == nil {
			d.boot = strings.TrimSpace(string(raw))
		}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-d.queue:
				if !deliverDiagnostic(ctx, sink, r.event) {
					r.trace.lost.Add(1)
				}
			}
		}
	}()
	return d
}

func deliverDiagnostic(ctx context.Context, sink func(context.Context, Event) error, event Event) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return sink != nil && sink(ctx, event) == nil
}

// Close stops accepting useful delivery without waiting for a diagnostic sink.
func (d *Diagnostics) Close() {
	if d != nil {
		d.cancel()
	}
}

type keeperTrace struct {
	mu                                         sync.Mutex
	diagnostics                                *Diagnostics
	identity                                   Identity
	instance                                   string
	sequence, renewal                          uint64
	sent, expires, deadline, executionDeadline time.Time
	inFlight                                   bool
	lost                                       atomic.Uint64
}

func (d *Diagnostics) start(ctx context.Context, identity Identity) *keeperTrace {
	if d == nil {
		return nil
	}
	deadline, _ := ctx.Deadline()
	executionDeadline, _ := ExecutionContext(ctx).Deadline()
	return &keeperTrace{diagnostics: d, identity: identity, instance: fmt.Sprintf("%d-%d-%d", os.Getpid(), d.origin.UnixNano(), d.instances.Add(1)), deadline: deadline, executionDeadline: executionDeadline}
}

func (t *keeperTrace) emit(kind string, sent, expires time.Time, outcome string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if kind == "renewal_start" {
		t.renewal++
		t.inFlight = true
		t.sent = sent
	}
	if kind == "renewal_finish" {
		t.inFlight = false
	}
	if !expires.IsZero() {
		t.expires = expires
	}
	t.sequence++
	before := time.Since(t.diagnostics.origin).Nanoseconds()
	wall := time.Now().UTC()
	after := time.Since(t.diagnostics.origin).Nanoseconds()
	e := Event{BootID: t.diagnostics.boot, Platform: runtime.GOOS, ProcessBirth: t.diagnostics.birth, IdentityComplete: t.diagnostics.birth != "" && (runtime.GOOS != "linux" || t.diagnostics.boot != ""), ClockOrigin: t.diagnostics.origin.UTC(), Identity: t.identity, Instance: t.instance, PID: os.Getpid(), Sequence: t.sequence, Dropped: t.lost.Load(), Kind: kind, MonoBefore: before, Wall: wall, MonoAfter: after, Sent: t.sent, ExpiresAt: t.expires, Deadline: t.deadline, ExecutionDeadline: t.executionDeadline, Renewal: t.renewal, InFlight: t.inFlight, Outcome: outcome}
	select {
	case <-t.diagnostics.ctx.Done():
		t.lost.Add(1)
		return
	default:
	}
	select {
	case t.diagnostics.queue <- diagnosticRecord{e, t}:
	default:
		t.lost.Add(1)
	}
}
