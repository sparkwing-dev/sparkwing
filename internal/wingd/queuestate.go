package wingd

import (
	"encoding/base64"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func stallRecoveryCommand(runID string) string {
	return fmt.Sprintf("sparkwing runs cancel --run %s", runID)
}

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
	capCores := float64(snap.TotalMilliCores) / 1000.0
	heldCores := float64(usedMilli) / 1000.0
	capMem := float64(snap.TotalMemoryBytes)
	heldMem := float64(usedMem)
	qs.Resources = append(qs.Resources,
		wingwire.ResourceState{
			Key:            "cores",
			Capacity:       capCores,
			Held:           heldCores,
			Reserved:       d.reservedCores,
			External:       d.externalCores,
			ExternalSource: externalSource(d.cpuMeasured),
			Available:      grantCores,
		},
		wingwire.ResourceState{
			Key:            "memory",
			Capacity:       capMem,
			Held:           heldMem,
			Reserved:       float64(d.reservedMem),
			External:       float64(d.externalMem),
			ExternalSource: externalSource(d.memMeasured),
			Available:      grantMem,
		},
	)
	if d.headroomInit {
		qs.ExternalSampleAgeMS = d.now().Sub(d.headroomAt).Milliseconds()
	}
	if !d.measuredAt.IsZero() {
		qs.ExternalMeasurementAgeMS = d.now().Sub(d.measuredAt).Milliseconds()
	}
	for _, ss := range snap.Semaphores {
		qs.Resources = append(qs.Resources, wingwire.ResourceState{
			Key:      ss.Key,
			Capacity: float64(effectiveCapacity(ss)),
			Held:     float64(semHeld[ss.Key]),
		})
	}

	now := d.now()
	for _, ls := range snap.Leases {
		rowID := queueRowIdentity(ls.RequestID, d.byRun[ls.RequestID])
		h := wingwire.Holder{
			RunID:         rowID.runID,
			ParticipantID: rowID.participantID,
			DisplayRunID:  rowID.displayRunID,
			Resources: wingwire.HostResources{
				Cores:       float64(ls.MilliCores) / 1000.0,
				MemoryBytes: int64(ls.MemoryBytes),
			},
			Semaphores:     claimKeys(ls.Claims),
			ConnectionOnly: !leaseHoldsResources(snap, ls),
		}
		if c := d.byRun[ls.RequestID]; c != nil {
			h.Pipeline = c.pipeline
			h.Repo = c.repo
			if !c.startAt.IsZero() {
				h.ElapsedMS = now.Sub(c.startAt).Milliseconds()
			}
			h.CostSource = c.costSource
			h.CostRationale = d.costRationale(c)
			h.ExpectedDurationMS = c.expectedDurationMS
			h.DriftWarning = c.driftWarning
			h.Origin = c.origin
			if c.stalled {
				h.Stalled = true
				h.Recovery = stallRecoveryCommand(rowID.runID)
			}
			if c.contended {
				h.Contended = true
				h.ContentionReason = c.contentionReason
			}
			if c.holdSampledMS > 0 {
				h.SaturatedShare = float64(c.holdSaturatedMS) / float64(c.holdSampledMS)
			}
		}
		qs.Holders = append(qs.Holders, h)
		qs.Holders = append(qs.Holders, d.attachedChildHoldersLocked(ls, now)...)
	}
	qs.Events = d.events.summary(now)
	if d.containerCores > 0 || d.containerMemory > 0 {
		qs.Container = &wingwire.ContainerLimit{
			Cores:           d.containerCores,
			MemoryBytes:     int64(d.containerMemory),
			HostCores:       d.hostCores,
			HostMemoryBytes: int64(d.hostMemory),
		}
	}
	rb := d.cfg.resolvedBudget()
	qs.Budget = &wingwire.BudgetState{
		Cores:              d.budgetCores,
		MachineCores:       d.machineCores,
		MemoryBytes:        int64(d.budgetMemory),
		MachineMemoryBytes: int64(d.machineMemory),
		Enforce:            d.cfg.Budget.Enforcing(),
		IgnoreExternal:     d.cfg.Budget.IgnoreExternal,
		Source:             string(rb.Source),
		Origin:             rb.Origin,
		Raw:                d.cfg.Budget.Raw,
	}
	qs.IgnoreExternal = d.cfg.Budget.IgnoreExternal
	qs.CapacityChange = d.capacityChange

	remaining := map[string]float64{}
	for _, r := range qs.Resources {
		if r.Key == "cores" || r.Key == "memory" {
			remaining[r.Key] = r.Available
		} else {
			remaining[r.Key] = r.Capacity - r.Held
		}
	}
	available := map[string]wingwire.ResourceState{}
	for _, r := range qs.Resources {
		if qs.IgnoreExternal {
			r.External = 0
		}
		available[r.Key] = r
	}
	if usedMilli == 0 {
		remaining["cores"] = capCores
		cores := available["cores"]
		cores.Available = capCores
		available["cores"] = cores
	}
	for i, w := range snap.Waiters {
		c := d.byRun[w.RequestID]
		rowID := queueRowIdentity(w.RequestID, c)
		rationale := d.costRationale(c)
		waiter := wingwire.Waiter{
			RunID:         rowID.runID,
			ParticipantID: rowID.participantID,
			DisplayRunID:  rowID.displayRunID,
			Position:      i + 1,
			Priority:      w.Priority,
			BackfillCount: w.BackfillCount,
			Resources: wingwire.HostResources{
				Cores:       float64(w.MilliCores) / 1000.0,
				MemoryBytes: int64(w.MemoryBytes),
			},
			Semaphores:     claimKeys(w.Claims),
			WaitingOn:      waitingOn(w, remaining),
			BlockingReason: hostBlockingReason(float64(w.MilliCores)/1000.0, float64(w.MemoryBytes), available, rationale),
			CostRationale:  rationale,
		}
		waiter.BlockingReason = queueBlockingReason(waiter.BlockingReason, waiter.WaitingOn, i+1)
		if w.BackfillCount > 0 {
			protection := fmt.Sprintf("protected from further backfill after %d younger grant(s)", w.BackfillCount)
			if waiter.BlockingReason == "" {
				waiter.BlockingReason = protection
			} else {
				waiter.BlockingReason += "; " + protection
			}
		}
		if c != nil {
			waiter.Pipeline = c.pipeline
			waiter.Repo = c.repo
			if !c.startAt.IsZero() {
				waiter.WaitingMS = now.Sub(c.startAt).Milliseconds()
			}
			waiter.CostSource = c.costSource
			waiter.ExpectedDurationMS = c.expectedDurationMS
			waiter.DriftWarning = c.driftWarning
			waiter.Origin = c.origin
		}
		qs.Waiters = append(qs.Waiters, waiter)
	}

	annotateAdmissionWaiting(&qs)
	annotateETA(&qs, snap)
	annotateSemaphoreETA(&qs, snap)
	return qs
}

func leaseHoldsResources(snap admission.Snapshot, ls admission.LeaseState) bool {
	if ls.MilliCores > 0 || ls.MemoryBytes > 0 {
		return true
	}
	for _, sem := range snap.Semaphores {
		for _, hold := range sem.Holds {
			if hold.Lease == ls.ID && !hold.Superseded && hold.Cost > 0 {
				return true
			}
		}
	}
	return false
}

func annotateAdmissionWaiting(qs *wingwire.QueueState) {
	byOwner := make(map[string][]string)
	for _, w := range qs.Waiters {
		if w.ParticipantID != "" {
			byOwner[w.RunID] = append(byOwner[w.RunID], w.ParticipantID)
		}
	}
	for i := range qs.Holders {
		h := &qs.Holders[i]
		if h.ParticipantID != "" || h.Parent != "" || h.Resources.Cores > 0 || h.Resources.MemoryBytes > 0 {
			continue
		}
		active := byOwner[h.RunID]
		if len(active) == 0 {
			continue
		}
		h.AdmissionWaiting = true
		h.ActiveWaiterParticipantIDs = append([]string(nil), active...)
		h.Stalled = false
		h.Recovery = ""
	}
}

func queueBlockingReason(hostReason string, waitingOn []string, position int) string {
	reason := hostReason
	for _, resource := range waitingOn {
		if resource == "cores" || resource == "memory" {
			continue
		}
		semaphoreReason := fmt.Sprintf("waiting for semaphore %q", resource)
		if reason == "" {
			reason = semaphoreReason
		} else {
			reason += "; " + semaphoreReason
		}
	}
	if reason != "" || position <= 1 {
		return reason
	}
	return "waiting behind earlier queued work"
}

func (d *Daemon) attachedChildHoldersLocked(ls admission.LeaseState, now time.Time) []wingwire.Holder {
	var out []wingwire.Holder
	for _, member := range ls.Members {
		if member == ls.RequestID {
			continue
		}
		c := d.byRun[member]
		childID := queueRowIdentity(member, c)
		parentID := queueRowIdentity(ls.RequestID, d.byRun[ls.RequestID])
		child := wingwire.Holder{
			RunID:               childID.runID,
			ParticipantID:       childID.participantID,
			DisplayRunID:        childID.displayRunID,
			Parent:              parentID.runID,
			ParentParticipantID: parentID.participantID,
		}
		if c != nil {
			child.Pipeline = c.pipeline
			child.Repo = c.repo
			child.Origin = c.origin
			if !c.startAt.IsZero() {
				child.ElapsedMS = now.Sub(c.startAt).Milliseconds()
			}
		}
		out = append(out, child)
	}
	return out
}

type queueIdentity struct {
	runID         string
	participantID string
	displayRunID  string
}

func queueRowIdentity(participantID string, c *conn) queueIdentity {
	runID := participantID
	displayRunID := ""
	if c != nil {
		if c.ownerRunID != "" {
			runID = c.ownerRunID
		}
		displayRunID = c.displayRunID
	}
	if runID == participantID {
		if owner, label, ok := decodeNodeParticipantID(participantID); ok {
			runID = owner
			if displayRunID == "" {
				displayRunID = label
			}
		}
	}
	id := queueIdentity{runID: runID, displayRunID: displayRunID}
	if participantID != runID {
		id.participantID = participantID
	}
	return id
}

func decodeNodeParticipantID(participantID string) (ownerRunID, displayRunID string, ok bool) {
	for _, marker := range []string{"/node-host/", "/node-semaphore/"} {
		owner, encoded, found := strings.Cut(participantID, marker)
		if !found || owner == "" || encoded == "" {
			continue
		}
		node, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return "", "", false
		}
		return owner, owner + "/" + string(node), true
	}
	return "", "", false
}

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
			cores:     w.Resources.Cores,
			softCores: snap.Waiters[i].SoftCores,
			mem:       float64(w.Resources.MemoryBytes),
			duration:  durationMS(w.ExpectedDurationMS),
		})
		waiterIdx = append(waiterIdx, i)
	}

	starts, clear := simulateQueue(capCores, capMem, holders, waiters)
	for j, orig := range waiterIdx {
		if ms, ok := finiteETA(starts[j]); ok {
			qs.Waiters[orig].ExpectedStartMS = &ms
		}
	}
	if ms, ok := finiteETA(clear); ok {
		qs.ExpectedClearMS = &ms
	}
}

func annotateSemaphoreETA(qs *wingwire.QueueState, snap admission.Snapshot) {
	starts, clear := simulateAdmissionETA(qs, snap)
	for i := range qs.Waiters {
		qs.Waiters[i].ExpectedStartMS = nil
		ms, ok := finiteETA(starts[i])
		if ok {
			qs.Waiters[i].ExpectedStartMS = &ms
		}
	}
	qs.ExpectedClearMS = nil
	if ms, ok := finiteETA(clear); ok {
		qs.ExpectedClearMS = &ms
	}
}

func semaphoreETACapacity(snap admission.Snapshot, key string) int {
	eff := 0
	for _, ss := range snap.Semaphores {
		if ss.Key == key {
			eff = effectiveCapacity(ss)
			break
		}
	}
	for _, w := range snap.Waiters {
		for _, c := range w.Claims {
			if c.Key == key && c.Capacity > 0 && (eff == 0 || c.Capacity < eff) {
				eff = c.Capacity
			}
		}
	}
	return eff
}

func semaphoreETAHolderRows(qs *wingwire.QueueState, snap admission.Snapshot) map[admission.LeaseID]wingwire.Holder {
	byParticipant := make(map[string]wingwire.Holder, len(qs.Holders))
	for _, h := range qs.Holders {
		id := h.ParticipantID
		if id == "" {
			id = h.RunID
		}
		byParticipant[id] = h
	}
	rows := make(map[admission.LeaseID]wingwire.Holder, len(snap.Leases))
	for _, ls := range snap.Leases {
		if h, ok := byParticipant[ls.RequestID]; ok {
			rows[ls.ID] = h
		}
	}
	return rows
}

type simRun struct {
	cores     float64
	softCores bool
	mem       float64
	finish    float64
	duration  float64
}

type simEvent struct {
	at    float64
	cores float64
	mem   float64
}

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
	blocked := false
	for i, w := range waiters {
		if blocked {
			starts[i] = math.Inf(1)
			clear = math.Inf(1)
			continue
		}
		if (!w.softCores && w.cores > capCores+eps) || w.mem > capMem+eps {
			starts[i] = math.Inf(1)
			clear = math.Inf(1)
			blocked = true
			continue
		}
		for !simFits(w, capCores, freeCores, freeMem, eps) {
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
			blocked = true
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

func simFits(w simRun, capCores, freeCores, freeMem, eps float64) bool {
	memOK := w.mem <= freeMem+eps
	if !memOK {
		return false
	}
	if w.cores <= eps {
		return true
	}
	if w.cores <= freeCores+eps {
		return true
	}
	return w.softCores && freeCores >= capCores-eps
}

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

func remainingMS(expectedMS, elapsedMS int64) float64 {
	if expectedMS <= 0 {
		return math.Inf(1)
	}
	rem := float64(expectedMS - elapsedMS)
	if rem <= 0 {
		return math.Inf(1)
	}
	return rem
}

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

func externalSource(measured bool) string {
	if measured {
		return wingwire.ExternalMeasured
	}
	return wingwire.ExternalUnmeasured
}

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

func (d *Daemon) hostBlockingReasonLocked(res wingwire.HostResources, rationale string) string {
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
	if usedMilli == 0 {
		grantCores = float64(snap.TotalMilliCores) / 1000.0
	}
	grantMem := float64(int64(minU64(snap.TotalMemoryBytes, snap.HeadroomMemoryBytes)) - int64(usedMem))
	if grantMem < 0 {
		grantMem = 0
	}
	extCores, extMem := d.externalCores, float64(d.externalMem)
	if d.cfg.Budget.IgnoreExternal {
		extCores, extMem = 0, 0
	}
	avail := map[string]wingwire.ResourceState{
		"cores":  {Key: "cores", Available: grantCores, External: extCores, ExternalSource: externalSource(d.cpuMeasured)},
		"memory": {Key: "memory", Available: grantMem, External: extMem, ExternalSource: externalSource(d.memMeasured)},
	}
	return hostBlockingReason(res.Cores, float64(res.MemoryBytes), avail, rationale)
}

func hostBlockingReason(needCores, needMem float64, available map[string]wingwire.ResourceState, rationale string) string {
	if needCores > 0 {
		if r, ok := available["cores"]; ok && r.Available < needCores {
			ext := ""
			if r.External > 0 && r.ExternalSource != wingwire.ExternalUnmeasured {
				ext = fmt.Sprintf(" (external load %s)", trimCores(r.External))
			}
			return fmt.Sprintf("needs %s cores%s; %s available%s", trimCores(needCores), costParen(rationale), trimCores(r.Available), ext)
		}
	}
	if needMem > 0 {
		if r, ok := available["memory"]; ok && r.Available < needMem {
			ext := ""
			if r.External > 0 && r.ExternalSource != wingwire.ExternalUnmeasured {
				ext = fmt.Sprintf(" (external load %s)", humanBytesShort(r.External))
			}
			return fmt.Sprintf("needs %s%s; %s available%s", humanBytesShort(needMem), costParen(rationale), humanBytesShort(r.Available), ext)
		}
	}
	return ""
}

func costParen(rationale string) string {
	if rationale == "" {
		return ""
	}
	return " (" + rationale + ")"
}

func (d *Daemon) costRationale(c *conn) string {
	if c == nil {
		return ""
	}
	return wingwire.CostRationale(wingwire.CostSource(c.costSource), c.sampleCount)
}

func trimCores(v float64) string { return fmt.Sprintf("%.1f", v) }

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
