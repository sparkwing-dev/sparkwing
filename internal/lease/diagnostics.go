package lease

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
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

// Summary closes one stream's observation window, from open to seal. Lost and Captured
// are instance totals; LastDeliveredSequence and DrainComplete describe this window only.
type Summary struct {
	OpenMonoBefore        int64     `json:"open_mono_before_ns"`
	OpenWall              time.Time `json:"open_wall"`
	OpenMonoAfter         int64     `json:"open_mono_after_ns"`
	SealMonoBefore        int64     `json:"seal_mono_before_ns"`
	SealWall              time.Time `json:"seal_wall"`
	SealMonoAfter         int64     `json:"seal_mono_after_ns"`
	AsOfSeal              bool      `json:"as_of_seal"`
	LastDeliveredSequence uint64    `json:"last_delivered_sequence"`
	Captured              uint64    `json:"captured"`
	Lost                  uint64    `json:"lost"`
	DrainComplete         bool      `json:"drain_complete"`
}

// Event reports best-effort observations; a seal summary bounds only its capture window.
type Event struct {
	Summary           *Summary  `json:"summary,omitempty"`
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
	// WatchRenewal identifies the renewal whose expiry the watcher installed.
	WatchRenewal uint64 `json:"watch_renewal"`
	// Renewal identifies the latest started request, which may still be in flight.
	Renewal  uint64 `json:"renewal"`
	InFlight bool   `json:"renewal_in_flight"`
	Outcome  string `json:"outcome,omitempty"`
}

var (
	processDiagnosticOrigin    = time.Now()
	processDiagnosticInstances atomic.Uint64
)

type diagnosticRecord struct {
	event Event
	trace *keeperTrace
}

type clockMark struct {
	before int64
	wall   time.Time
	after  int64
}

type sealRequest struct {
	opened, cutoff clockMark
	ctx            context.Context
	sink           func(context.Context, Event) error
	traces         []*keeperTrace
	result         chan bool
}

// safety: this bit on the active-capture count ends admission, so captures that entered before it can be awaited.
const captureClosed = uint64(1) << 63

// Diagnostics isolates best-effort delivery from renewal and expiry. Close cancels delivery;
// a sink must honor its context. A blocked or failing sink cannot block a keeper.
type Diagnostics struct {
	lifecycle sync.Mutex
	traces    []*keeperTrace
	opened    clockMark
	cutoff    *clockMark
	sealing   bool
	sealed    bool
	reopen    bool
	active    atomic.Uint64
	idle      chan struct{}
	seals     chan sealRequest
	queue     chan diagnosticRecord
	ctx       context.Context
	done      <-chan struct{}
	cancel    context.CancelFunc
	origin    time.Time
	birth     string
	boot      string
}

// NewDiagnostics starts one bounded delivery worker. The sink receives no execution authority.
// The first observation window opens immediately.
func NewDiagnostics(parent context.Context, sink func(context.Context, Event) error) *Diagnostics {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	d := &Diagnostics{queue: make(chan diagnosticRecord, 256), ctx: ctx, done: ctx.Done(), cancel: cancel, origin: processDiagnosticOrigin, idle: make(chan struct{}, 1), seals: make(chan sealRequest, 1)}
	d.opened = d.mark()
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
			case request := <-d.seals:
				d.seal(request, nil)
			case <-d.done:
				select {
				case request := <-d.seals:
					d.seal(request, nil)
				default:
				}
				return
			case r := <-d.queue:
				select {
				case request := <-d.seals:
					d.seal(request, &r)
					continue
				default:
				}
				d.deliver(d.ctx, sink, r)
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
	watchRenewal                               uint64
	mu                                         sync.Mutex
	diagnostics                                *Diagnostics
	identity                                   Identity
	instance                                   string
	sequence, renewal                          uint64
	sent, expires, deadline, executionDeadline time.Time
	inFlight                                   bool
	lost                                       atomic.Uint64
	lostAtOpen                                 atomic.Uint64
	delivered                                  atomic.Uint64
}

func (d *Diagnostics) start(ctx context.Context, identity Identity) *keeperTrace {
	if d == nil {
		return nil
	}
	deadline, _ := ctx.Deadline()
	executionDeadline, _ := ExecutionContext(ctx).Deadline()
	trace := &keeperTrace{diagnostics: d, identity: identity, instance: fmt.Sprintf("%d-%d-%d", os.Getpid(), d.origin.UnixNano(), processDiagnosticInstances.Add(1)), deadline: deadline, executionDeadline: executionDeadline}
	d.lifecycle.Lock()
	defer d.lifecycle.Unlock()
	d.traces = append(d.traces, trace)
	return trace
}

// safety: emit never waits; between a cutoff and the next open it updates keeper state but captures nothing.
func (t *keeperTrace) emit(kind string, sent, expires time.Time, outcome string, watchRenewal uint64) uint64 {
	if t == nil {
		return 0
	}
	d := t.diagnostics
	closed := (d.active.Add(1)-1)&captureClosed != 0
	defer func() {
		if d.active.Add(^uint64(0))&^captureClosed == 0 {
			select {
			case d.idle <- struct{}{}:
			default:
			}
		}
	}()
	if !t.mu.TryLock() {
		if !closed {
			t.lost.Add(1)
		}
		return 0
	}
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
	if kind == "watch_reset" {
		t.watchRenewal = watchRenewal
	}
	if closed {
		return t.renewal
	}
	t.sequence++
	before := time.Since(t.diagnostics.origin).Nanoseconds()
	wall := time.Now().UTC()
	after := time.Since(t.diagnostics.origin).Nanoseconds()
	e := Event{WatchRenewal: t.watchRenewal, BootID: t.diagnostics.boot, Platform: runtime.GOOS, ProcessBirth: t.diagnostics.birth, IdentityComplete: t.diagnostics.birth != "" && (runtime.GOOS != "linux" || t.diagnostics.boot != ""), ClockOrigin: t.diagnostics.origin.UTC(), Identity: t.identity, Instance: t.instance, PID: os.Getpid(), Sequence: t.sequence, Dropped: t.lost.Load(), Kind: kind, MonoBefore: before, Wall: wall, MonoAfter: after, Sent: t.sent, ExpiresAt: t.expires, Deadline: t.deadline, ExecutionDeadline: t.executionDeadline, Renewal: t.renewal, InFlight: t.inFlight, Outcome: outcome}
	select {
	case <-t.diagnostics.done:
		t.lost.Add(1)
		return t.renewal
	default:
	}
	select {
	case t.diagnostics.queue <- diagnosticRecord{e, t}:
	default:
		t.lost.Add(1)
	}
	return t.renewal
}

func (d *Diagnostics) deliver(ctx context.Context, sink func(context.Context, Event) error, r diagnosticRecord) {
	if !deliverDiagnostic(ctx, sink, r.event) {
		r.trace.lost.Add(1)
	} else {
		r.trace.delivered.Store(r.event.Sequence)
	}
}

func (d *Diagnostics) mark() clockMark {
	before := time.Since(d.origin).Nanoseconds()
	wall := time.Now().UTC()
	return clockMark{before: before, wall: wall, after: time.Since(d.origin).Nanoseconds()}
}

// Cutoff ends capture admission for the current window. It neither waits for queued
// delivery nor stops a keeper; renewal and expiry continue unobserved until Open.
func (d *Diagnostics) Cutoff() {
	if d == nil {
		return
	}
	d.lifecycle.Lock()
	defer d.lifecycle.Unlock()
	d.cutoffLocked()
}

func (d *Diagnostics) cutoffLocked() {
	if d.cutoff != nil {
		return
	}
	cutoff := d.mark()
	d.active.Or(captureClosed)
	d.cutoff = &cutoff
}

// Seal cuts off the current window, drains the observations it admitted through sink and
// then writes one summary per keeper instance. It reports whether every summary landed and
// the window lost nothing. The sink must append only to the stream that window was bound to
// and must honor its context: when ctx ends first, Seal returns false, remaining records
// count as lost and no summary is written. Each window seals at most once.
func (d *Diagnostics) Seal(ctx context.Context, sink func(context.Context, Event) error) bool {
	if d == nil {
		return false
	}
	d.lifecycle.Lock()
	if d.sealing || d.sealed || d.seals == nil {
		d.lifecycle.Unlock()
		return false
	}
	d.cutoffLocked()
	d.sealing = true
	request := sealRequest{opened: d.opened, cutoff: *d.cutoff, ctx: ctx, sink: sink, traces: slices.Clone(d.traces), result: make(chan bool, 1)}
	// safety: sealing admits one request until the worker finishes it, so this send never blocks.
	d.seals <- request
	d.lifecycle.Unlock()
	select {
	case ok := <-request.result:
		return ok && ctx.Err() == nil
	case <-ctx.Done():
		return false
	}
}

// Open starts the next window when a new stream binds. A window still sealing opens its
// successor only after it finishes, so no admitted record can reach the wrong stream.
func (d *Diagnostics) Open() {
	if d == nil {
		return
	}
	d.lifecycle.Lock()
	defer d.lifecycle.Unlock()
	if d.cutoff == nil {
		return
	}
	if d.sealing || !d.sealed {
		d.reopen = true
		return
	}
	d.openLocked()
}

func (d *Diagnostics) openLocked() {
	d.reopen, d.sealed, d.cutoff = false, false, nil
	d.opened = d.mark()
	for _, trace := range d.traces {
		trace.delivered.Store(0)
		trace.lostAtOpen.Store(trace.lost.Load())
	}
	d.active.And(^captureClosed)
}

func (d *Diagnostics) seal(request sealRequest, pending *diagnosticRecord) {
	// safety: captures never wait, so this ends promptly; a capture admitted before the cutoff
	// must be queued before leftovers are discarded or it would leak into the next window.
	for d.active.Load()&^captureClosed != 0 {
		<-d.idle
	}
	records := make([]diagnosticRecord, 0, 1)
	if pending != nil {
		records = append(records, *pending)
	}
	for drained := false; !drained; {
		select {
		case r := <-d.queue:
			records = append(records, r)
		default:
			drained = true
		}
	}
	for _, r := range records {
		if request.ctx.Err() != nil {
			r.trace.lost.Add(1)
			continue
		}
		d.deliver(request.ctx, request.sink, r)
	}
	ok := d.summaries(request)
	d.lifecycle.Lock()
	d.sealing, d.sealed = false, true
	if d.reopen {
		d.openLocked()
	}
	d.lifecycle.Unlock()
	request.result <- ok
}

func (d *Diagnostics) summaries(request sealRequest) bool {
	complete := true
	for _, trace := range request.traces {
		// safety: Captures admitted before the cutoff are queued and delivered, so sequence is stable.
		lost := trace.lost.Load()
		summary := &Summary{
			OpenMonoBefore: request.opened.before, OpenWall: request.opened.wall, OpenMonoAfter: request.opened.after,
			SealMonoBefore: request.cutoff.before, SealWall: request.cutoff.wall, SealMonoAfter: request.cutoff.after,
			AsOfSeal: true, LastDeliveredSequence: trace.delivered.Load(), Captured: trace.sequence, Lost: lost, DrainComplete: lost == trace.lostAtOpen.Load(),
		}
		now := d.mark()
		event := Event{
			Summary: summary, Kind: "summary", Identity: trace.identity, Instance: trace.instance,
			PID: os.Getpid(), ProcessBirth: d.birth, BootID: d.boot, Platform: runtime.GOOS,
			IdentityComplete: d.birth != "" && (runtime.GOOS != "linux" || d.boot != ""), ClockOrigin: d.origin.UTC(),
			Sequence: trace.sequence, Dropped: lost, MonoBefore: now.before, Wall: now.wall, MonoAfter: now.after,
		}
		if request.ctx.Err() != nil || !deliverDiagnostic(request.ctx, request.sink, event) {
			trace.lost.Add(1)
			complete = false
			continue
		}
		complete = complete && summary.DrainComplete
	}
	return complete
}
