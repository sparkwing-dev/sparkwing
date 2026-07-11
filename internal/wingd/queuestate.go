package wingd

import (
	"fmt"
	"math"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// stallRecoveryCommand is the single non-destructive verb a queue view
// advertises for a wedged holder. It cancels one run by id and never
// touches shared host state.
func stallRecoveryCommand(runID string) string {
	return fmt.Sprintf("sparkwing runs cancel --run %s", runID)
}

// buildQueueStateLocked renders the current admission picture for the
// read-only queue view: capacity rows with held amounts, holders with
// elapsed time and cost, and waiters in admission order, each annotated
// with its cost source, drift warning, and -- for waiters -- an estimated
// start time. The caller holds d.mu.
func (d *Daemon) buildQueueStateLocked() wingwire.QueueState {
	snap := d.ledger.Snapshot()
	var qs wingwire.QueueState
	qs.DaemonVersion = d.cfg.Version
	if !d.startedAt.IsZero() {
		qs.DaemonUptimeMS = d.now().Sub(d.startedAt).Milliseconds()
	}

	var usedMilli int64
	var usedMem uint64
	semHeld := map[string]int{}
	for _, ls := range snap.Leases {
		usedMilli += ls.MilliCores
		usedMem += ls.MemoryBytes
	}
	for _, ss := range snap.Semaphores {
		held := 0
		for _, h := range ss.Holds {
			if !h.Superseded {
				held += h.Cost
			}
		}
		semHeld[ss.Key] = held
	}

	grantCores := float64(min64(snap.TotalMilliCores, snap.HeadroomMilliCores)-usedMilli) / 1000.0
	if grantCores < 0 {
		grantCores = 0
	}
	grantMem := float64(int64(minU64(snap.TotalMemoryBytes, snap.HeadroomMemoryBytes)) - int64(usedMem))
	if grantMem < 0 {
		grantMem = 0
	}
	qs.Resources = append(qs.Resources,
		wingwire.ResourceState{
			Key:       "cores",
			Capacity:  float64(snap.TotalMilliCores) / 1000.0,
			Held:      float64(usedMilli) / 1000.0,
			Reserved:  d.reservedCores,
			External:  d.externalCores,
			Available: grantCores,
		},
		wingwire.ResourceState{
			Key:       "memory",
			Capacity:  float64(snap.TotalMemoryBytes),
			Held:      float64(usedMem),
			Reserved:  float64(d.reservedMem),
			External:  float64(d.externalMem),
			Available: grantMem,
		},
	)
	for _, ss := range snap.Semaphores {
		qs.Resources = append(qs.Resources, wingwire.ResourceState{
			Key:      ss.Key,
			Capacity: float64(effectiveCapacity(ss)),
			Held:     float64(semHeld[ss.Key]),
		})
	}

	now := d.now()
	for _, ls := range snap.Leases {
		h := wingwire.Holder{
			RunID: ls.RequestID,
			Resources: wingwire.HostResources{
				Cores:       float64(ls.MilliCores) / 1000.0,
				MemoryBytes: int64(ls.MemoryBytes),
			},
			Semaphores: claimKeys(ls.Claims),
		}
		if c := d.byRun[ls.RequestID]; c != nil {
			h.Pipeline = c.pipeline
			h.Repo = c.repo
			if !c.startAt.IsZero() {
				h.ElapsedMS = now.Sub(c.startAt).Milliseconds()
			}
			h.CostSource = c.costSource
			h.ExpectedDurationMS = c.expectedDurationMS
			h.DriftWarning = c.driftWarning
			if c.stalled {
				h.Stalled = true
				h.Recovery = stallRecoveryCommand(ls.RequestID)
			}
		}
		qs.Holders = append(qs.Holders, h)
		qs.Holders = append(qs.Holders, d.attachedChildHoldersLocked(ls, now)...)
	}
	qs.Events = d.events.summary(now)

	remaining := map[string]float64{}
	for _, r := range qs.Resources {
		remaining[r.Key] = r.Capacity - r.Held
	}
	available := map[string]wingwire.ResourceState{}
	for _, r := range qs.Resources {
		available[r.Key] = r
	}
	for i, w := range snap.Waiters {
		waiter := wingwire.Waiter{
			RunID:    w.RequestID,
			Position: i + 1,
			Resources: wingwire.HostResources{
				Cores:       float64(w.MilliCores) / 1000.0,
				MemoryBytes: int64(w.MemoryBytes),
			},
			Semaphores:     claimKeys(w.Claims),
			WaitingOn:      waitingOn(w, remaining),
			BlockingReason: hostBlockingReason(float64(w.MilliCores)/1000.0, float64(w.MemoryBytes), available),
		}
		if c := d.byRun[w.RequestID]; c != nil {
			waiter.Pipeline = c.pipeline
			waiter.Repo = c.repo
			if !c.startAt.IsZero() {
				waiter.WaitingMS = now.Sub(c.startAt).Milliseconds()
			}
			waiter.CostSource = c.costSource
			waiter.ExpectedDurationMS = c.expectedDurationMS
			waiter.DriftWarning = c.driftWarning
		}
		qs.Waiters = append(qs.Waiters, waiter)
	}

	annotateETA(&qs, snap)
	return qs
}

// attachedChildHoldersLocked renders the child runs riding a lease as
// zero-cost holders under their parent, so an attached child appears in
// the queue as what it is rather than a run holding nothing. Members are
// walked in the lease's stored order; the lease's own requester is
// skipped. The caller holds d.mu.
func (d *Daemon) attachedChildHoldersLocked(ls admission.LeaseState, now time.Time) []wingwire.Holder {
	var out []wingwire.Holder
	for _, member := range ls.Members {
		if member == ls.RequestID {
			continue
		}
		child := wingwire.Holder{RunID: member, Parent: ls.RequestID}
		if c := d.byRun[member]; c != nil {
			child.Pipeline = c.pipeline
			child.Repo = c.repo
			if !c.startAt.IsZero() {
				child.ElapsedMS = now.Sub(c.startAt).Milliseconds()
			}
		}
		out = append(out, child)
	}
	return out
}

// annotateETA fills each waiter's ExpectedStartMS and the queue's
// ExpectedClearMS by simulating the FIFO queue with measured durations and
// costs. Capacity is the grantable ceiling (total capped by headroom).
// Only host-drawing runs are simulated: a semaphore-only hold or wait
// (zero cores and memory) draws no host budget, so it neither gates host
// admission nor bounds the clear time. A run whose duration is unknown
// never finishes in the simulation, so any estimate that would depend on
// it is left nil rather than fabricated.
func annotateETA(qs *wingwire.QueueState, snap admission.Snapshot) {
	capCores := float64(min64(snap.TotalMilliCores, snap.HeadroomMilliCores)) / 1000.0
	capMem := float64(minU64(snap.TotalMemoryBytes, snap.HeadroomMemoryBytes))

	var holders []simRun
	for _, h := range qs.Holders {
		if h.Resources.Cores <= 0 && h.Resources.MemoryBytes <= 0 {
			continue
		}
		holders = append(holders, simRun{
			cores:  h.Resources.Cores,
			mem:    float64(h.Resources.MemoryBytes),
			finish: remainingMS(h.ExpectedDurationMS, h.ElapsedMS),
		})
	}
	var waiters []simRun
	var waiterIdx []int
	for i, w := range qs.Waiters {
		if w.Resources.Cores <= 0 && w.Resources.MemoryBytes <= 0 {
			continue
		}
		waiters = append(waiters, simRun{
			cores:    w.Resources.Cores,
			mem:      float64(w.Resources.MemoryBytes),
			duration: durationMS(w.ExpectedDurationMS),
		})
		waiterIdx = append(waiterIdx, i)
	}

	starts, clear := simulateQueue(capCores, capMem, holders, waiters)
	for j, orig := range waiterIdx {
		if !math.IsInf(starts[j], 1) {
			ms := int64(starts[j])
			qs.Waiters[orig].ExpectedStartMS = &ms
		}
	}
	if !math.IsInf(clear, 1) {
		ms := int64(clear)
		qs.ExpectedClearMS = &ms
	}
}

// simRun is one run in the ETA simulation. finish is a holder's remaining
// milliseconds; duration is a waiter's run length. Either is +Inf when the
// run's duration is unmeasured.
type simRun struct {
	cores    float64
	mem      float64
	finish   float64
	duration float64
}

// simEvent is a scheduled resource release at a point in simulated time.
type simEvent struct {
	at    float64
	cores float64
	mem   float64
}

// simulateQueue advances a FIFO admission queue in simulated time and
// returns each waiter's estimated start offset (ms from now) and the time
// the queue fully clears. An unmeasured duration propagates as +Inf: a
// waiter that must wait behind it starts at +Inf, and the clear time is
// +Inf when any run never finishes.
func simulateQueue(capCores, capMem float64, holders, waiters []simRun) (starts []float64, clear float64) {
	const eps = 1e-9
	freeCores := capCores
	freeMem := capMem
	var events []simEvent
	clear = 0
	for _, h := range holders {
		freeCores -= h.cores
		freeMem -= h.mem
		events = append(events, simEvent{at: h.finish, cores: h.cores, mem: h.mem})
		clear = math.Max(clear, h.finish)
	}

	starts = make([]float64, len(waiters))
	now := 0.0
	for i, w := range waiters {
		if w.cores > capCores+eps || w.mem > capMem+eps {
			starts[i] = math.Inf(1)
			continue
		}
		for w.cores > freeCores+eps || w.mem > freeMem+eps {
			e, ok := popEarliest(&events)
			if !ok {
				now = math.Inf(1)
				break
			}
			now = e.at
			freeCores += e.cores
			freeMem += e.mem
		}
		starts[i] = now
		if math.IsInf(now, 1) {
			clear = math.Inf(1)
			continue
		}
		freeCores -= w.cores
		freeMem -= w.mem
		finish := now + w.duration
		events = append(events, simEvent{at: finish, cores: w.cores, mem: w.mem})
		clear = math.Max(clear, finish)
	}
	return starts, clear
}

// popEarliest removes and returns the event with the smallest time.
func popEarliest(events *[]simEvent) (simEvent, bool) {
	es := *events
	if len(es) == 0 {
		return simEvent{}, false
	}
	minIdx := 0
	for i, e := range es {
		if e.at < es[minIdx].at {
			minIdx = i
		}
	}
	e := es[minIdx]
	*events = append(es[:minIdx], es[minIdx+1:]...)
	return e, true
}

// remainingMS is a holder's estimated milliseconds left: its measured p50
// minus elapsed, floored at zero. An unmeasured duration is +Inf.
func remainingMS(expectedMS, elapsedMS int64) float64 {
	if expectedMS <= 0 {
		return math.Inf(1)
	}
	rem := float64(expectedMS - elapsedMS)
	if rem < 0 {
		return 0
	}
	return rem
}

// durationMS is a waiter's measured run length, or +Inf when unmeasured.
func durationMS(expectedMS int64) float64 {
	if expectedMS <= 0 {
		return math.Inf(1)
	}
	return float64(expectedMS)
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func minU64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// waitingOn names the resources a waiter cannot fit into right now: host
// dimensions and full semaphore keys whose remaining room is smaller than
// what the waiter draws. An empty result means the waiter is blocked only
// by arrival order behind a heavier request ahead of it.
func waitingOn(w admission.WaiterState, remaining map[string]float64) []string {
	var keys []string
	if cores := float64(w.MilliCores) / 1000.0; cores > 0 && remaining["cores"] < cores {
		keys = append(keys, "cores")
	}
	if mem := float64(w.MemoryBytes); mem > 0 && remaining["memory"] < mem {
		keys = append(keys, "memory")
	}
	for _, c := range w.Claims {
		if room, ok := remaining[c.Key]; ok && room < float64(c.Cost) {
			keys = append(keys, c.Key)
		}
	}
	return keys
}

// effectiveCapacity is the smallest capacity any live hold declares for a
// semaphore, matching the ledger's most-restrictive-wins rule.
func effectiveCapacity(ss admission.SemaphoreState) int {
	eff := 0
	for _, h := range ss.Holds {
		if h.Superseded {
			continue
		}
		if eff == 0 || h.Capacity < eff {
			eff = h.Capacity
		}
	}
	if eff == 0 {
		return ss.LastCapacity
	}
	return eff
}

// hostBlockingReasonLocked renders the host-pressure blocking reason for a
// run charged res against the daemon's current headroom. Empty when host
// capacity is not what holds the run back. The caller holds d.mu.
func (d *Daemon) hostBlockingReasonLocked(res wingwire.HostResources) string {
	if res.Cores <= 0 && res.MemoryBytes <= 0 {
		return ""
	}
	snap := d.ledger.Snapshot()
	var usedMilli int64
	var usedMem uint64
	for _, ls := range snap.Leases {
		usedMilli += ls.MilliCores
		usedMem += ls.MemoryBytes
	}
	grantCores := float64(min64(snap.TotalMilliCores, snap.HeadroomMilliCores)-usedMilli) / 1000.0
	if grantCores < 0 {
		grantCores = 0
	}
	grantMem := float64(int64(minU64(snap.TotalMemoryBytes, snap.HeadroomMemoryBytes)) - int64(usedMem))
	if grantMem < 0 {
		grantMem = 0
	}
	avail := map[string]wingwire.ResourceState{
		"cores":  {Key: "cores", Available: grantCores, External: d.externalCores},
		"memory": {Key: "memory", Available: grantMem, External: float64(d.externalMem)},
	}
	return hostBlockingReason(res.Cores, float64(res.MemoryBytes), avail)
}

// hostBlockingReason renders the one-line reason a run cannot be admitted
// on host capacity right now, comparing what it needs against what is
// grantable and naming external load when it is the binding constraint.
// Cores bind before memory. Empty when neither host dimension blocks the
// run (a pure semaphore or arrival-order wait).
func hostBlockingReason(needCores, needMem float64, available map[string]wingwire.ResourceState) string {
	if needCores > 0 {
		if r, ok := available["cores"]; ok && r.Available < needCores {
			ext := ""
			if r.External > 0 {
				ext = fmt.Sprintf(" (external load %s)", trimCores(r.External))
			}
			return fmt.Sprintf("needs %s cores; %s available%s", trimCores(needCores), trimCores(r.Available), ext)
		}
	}
	if needMem > 0 {
		if r, ok := available["memory"]; ok && r.Available < needMem {
			ext := ""
			if r.External > 0 {
				ext = fmt.Sprintf(" (external load %s)", humanBytesShort(r.External))
			}
			return fmt.Sprintf("needs %s; %s available%s", humanBytesShort(needMem), humanBytesShort(r.Available), ext)
		}
	}
	return ""
}

// trimCores formats a core count with a single decimal place.
func trimCores(v float64) string { return fmt.Sprintf("%.1f", v) }

// humanBytesShort renders a byte count in the largest binary unit that
// keeps it readable, for blocking-reason strings.
func humanBytesShort(v float64) string {
	const unit = 1024.0
	if v < unit {
		return fmt.Sprintf("%.0fB", v)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	n := v
	i := -1
	for n >= unit && i < len(units)-1 {
		n /= unit
		i++
	}
	return fmt.Sprintf("%.1f%s", n, units[i])
}

func claimKeys(claims []admission.ClaimState) []string {
	if len(claims) == 0 {
		return nil
	}
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		out = append(out, c.Key)
	}
	return out
}
