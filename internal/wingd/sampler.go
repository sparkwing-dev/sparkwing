package wingd

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

type HostStat struct {
	TotalCores float64

	TotalMemoryBytes uint64

	LoadAverage float64

	BusyCores float64

	FreeMemoryBytes uint64

	LoadMeasured bool

	CPUMeasured bool

	MemoryMeasured bool
}

type HostSampler interface {
	Sample() (HostStat, error)
}

type pairedHostOwnedSampler interface {
	SampleWithOwned(roots []OwnedRoot, arbitratedCores float64) (HostStat, map[int]float64, bool, error)
}

type hostSamplerOnly struct {
	HostSampler
}

type platformSampler struct {
	cpu cpuTracker

	darwinPrev   map[int]darwinCPUProcess
	darwinPrevAt time.Time
}

func (p *platformSampler) Sample() (HostStat, error) {
	stat, err := sampleHost()
	if err != nil {
		return stat, err
	}
	stat.BusyCores, stat.CPUMeasured = p.cpu.busyCores(stat.TotalCores)
	return stat, nil
}

type ProcSampler interface {
	CPUUsage(pid int) (ProcUsage, bool)
}

type ProcBatchSampler interface {
	CPUUsages(pids []int) map[int]ProcUsage
}

// OwnedRoot is one run's root process and the moment the daemon began holding
// that run, which bounds how old the run's processes can be.
type OwnedRoot struct {
	PID       int
	HeldSince time.Time
}

// OwnedCPUSampler reports the CPU each held run's process tree ran, keyed by
// the root pid the caller asked about. A process under more than one root
// counts once, against its nearest ancestor root.
//
// An implementation leaves a root out of the map where no figure could be
// computed for it, and gives it a zero only where it ran no measurable CPU.
// Both answers grant the same cores, so a zero in place of an absence costs no
// capacity and reports the daemon as measuring cleanly while it is not.
//
// measured reports whether the host's process table was read at all. It is
// false where the read failed, and also where the platform has no way to
// measure owned CPU; either leaves byRoot empty and says nothing about any
// individual root. A platform answering false forever states a capability
// rather than a fault on the box, so a count tracking every reading there is
// the expected shape. An empty map with measured true says the table was read
// and every root in it was individually unreadable, a different fault.
//
// arbitratedCores is the capacity these runs physically execute on: a cgroup
// limit where one caps them below the machine's core count, and the machine's
// otherwise. An implementation bounds a figure it cannot otherwise justify
// against it, because no tree under that limit can have run more.
type OwnedCPUSampler interface {
	CPUUsage(roots []OwnedRoot, arbitratedCores float64) (byRoot map[int]float64, measured bool)
}

type ownedProcSampler struct {
	//lint:ignore U1000 used by platform implementations
	mu        sync.Mutex
	last      map[processIdentity]cpuSample
	lastAt    time.Time
	seenSince time.Time
}

func newOwnedCPUSampler() *ownedProcSampler {
	return &ownedProcSampler{last: map[processIdentity]cpuSample{}}
}

func (s *ownedProcSampler) CPUUsage(roots []OwnedRoot, arbitratedCores float64) (map[int]float64, bool) {
	if len(roots) == 0 {
		// safety: the samples and the clock they are read against move together.
		// Keeping either across a stretch with nothing held leaves this window open
		// across intervals nobody was watching a tree, and holding one again then
		// charges it that whole stretch in a single reading.
		s.forgetSamples(time.Now())
		return nil, true
	}
	return s.sampleOwned(roots, arbitratedCores)
}

func newProcSampler() *procSampler {
	return &procSampler{
		last: map[int]cpuSample{},
		tree: map[int]map[int]struct{}{},
	}
}

func (p *procSampler) CPUUsage(pid int) (ProcUsage, bool) { return p.sample(pid) }

func (p *procSampler) CPUUsages(pids []int) map[int]ProcUsage { return p.sampleMany(pids) }

type processIdentity struct {
	pid        int
	startTicks uint64
}

type ownedProcess struct {
	parentPID  int
	identity   processIdentity
	cpuSeconds float64
	startedAt  time.Time
}

func ownersByNearestRoot(parentOf map[int]int, rootPIDs map[int]struct{}) map[int]int {
	owners := make(map[int]int, len(parentOf))
	unowned := make(map[int]struct{}, len(parentOf))
	var chain []int
	for pid := range parentOf {
		chain = chain[:0]
		root, found := 0, false
		for current := pid; ; {
			if known, ok := owners[current]; ok {
				root, found = known, true
				break
			}
			if _, ok := unowned[current]; ok {
				break
			}
			if _, ok := rootPIDs[current]; ok {
				root, found = current, true
				chain = append(chain, current)
				break
			}
			chain = append(chain, current)
			next, ok := parentOf[current]
			if !ok || next <= 0 || len(chain) > len(parentOf) {
				break
			}
			current = next
		}
		for _, walked := range chain {
			if found {
				owners[walked] = root
			} else {
				unowned[walked] = struct{}{}
			}
		}
	}
	return owners
}

func ownedProcessOwners(roots []OwnedRoot, processes map[int]ownedProcess) map[processIdentity]int {
	parentOf := make(map[int]int, len(processes))
	for processID, process := range processes {
		parentOf[processID] = process.parentPID
	}
	rootPIDs := make(map[int]struct{}, len(roots))
	for _, root := range roots {
		if _, ok := processes[root.PID]; ok {
			rootPIDs[root.PID] = struct{}{}
		}
	}
	byPID := ownersByNearestRoot(parentOf, rootPIDs)
	owners := make(map[processIdentity]int, len(byPID))
	for processID, root := range byPID {
		owners[processes[processID].identity] = root
	}
	return owners
}

func (s *ownedProcSampler) forgetSamples(now time.Time) {
	// safety: the samples and the clock they are read against move together, so
	// no later window can span a stretch this sampler sat out.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, s.lastAt, s.seenSince = nil, now, now
}

// safety: one argument, because two adjacent times of the same type invite a
// transposition that dates against the scan's end.
type scanWindow struct {
	startedListingAt time.Time
	readAt           time.Time
}

func (s *ownedProcSampler) creditScan(
	processes map[int]ownedProcess,
	roots []OwnedRoot,
	window scanWindow,
	arbitratedCores float64,
) map[int]float64 {
	owners := ownedProcessOwners(roots, processes)
	s.mu.Lock()
	defer s.mu.Unlock()
	// safety: both ends of every window come from readAt, so the offset between it and
	// the counter reads cancels across readings.
	byRoot, next := ownedCPUByRoot(s.last, processes, owners, roots, s.lastAt, s.seenSince, window.readAt, arbitratedCores)
	s.last, s.lastAt, s.seenSince = next, window.readAt, window.startedListingAt
	return byRoot
}

func ownedCPUByRoot(
	previous map[processIdentity]cpuSample,
	processes map[int]ownedProcess,
	owners map[processIdentity]int,
	roots []OwnedRoot,
	lastAt time.Time,
	seenSince time.Time,
	now time.Time,
	arbitratedCores float64,
) (map[int]float64, map[processIdentity]cpuSample) {
	// safety: with no reading behind it there is no window, and a process's
	// counter says nothing about when its CPU ran. Leaving the window at zero
	// is what sends such a root out of the map rather than crediting it a
	// figure measured against the daemon's own start.
	window := 0.0
	if !lastAt.IsZero() {
		window = now.Sub(lastAt).Seconds()
	}
	creditable := creditableRoots(previous, processes, roots, lastAt, now)
	next := make(map[processIdentity]cpuSample, len(owners))
	byRoot := make(map[int]float64, len(owners))
	firstSight := make(map[int]float64, len(owners))
	unreadable := map[int]struct{}{}
	for identity, root := range owners {
		process, ok := processes[identity.pid]
		if !ok {
			continue
		}
		next[identity] = cpuSample{cpuSeconds: process.cpuSeconds, at: now}
		if _, ok := creditable[root]; !ok {
			continue
		}
		prior, seen := previous[identity]
		if !seen {
			if !startedInWindow(process.startedAt, seenSince, now) {
				// safety: a counter covering time nobody watched charges this window for
				// CPU that ran outside it.
				unreadable[root] = struct{}{}
				continue
			}
			firstSight[root] += process.cpuSeconds
			continue
		}
		wall := now.Sub(prior.at).Seconds()
		delta := process.cpuSeconds - prior.cpuSeconds
		if wall <= 0 || delta < 0 {
			// safety: a counter that stood still or ran backwards says nothing about
			// this process, and the rest of the tree's figure without it is short by an
			// unknown amount. A short figure still subtracts from external, so the tree
			// goes unmeasured rather than admitting against CPU nobody could read.
			unreadable[root] = struct{}{}
			continue
		}
		byRoot[root] += delta / wall
	}
	for root, cpuSeconds := range firstSight {
		credit, ok := firstSightCredit(cpuSeconds, window, arbitratedCores)
		if !ok {
			delete(byRoot, root)
			continue
		}
		byRoot[root] += credit
	}
	for root := range unreadable {
		delete(byRoot, root)
	}
	measureUnownedSurvivors(previous, processes, next, now)
	return byRoot, next
}

func measureUnownedSurvivors(
	previous map[processIdentity]cpuSample,
	processes map[int]ownedProcess,
	next map[processIdentity]cpuSample,
	now time.Time,
) {
	// safety: a process the daemon stopped owning is still measured while it
	// runs, so re-holding its tree charges only the CPU this reading covers.
	for identity := range previous {
		if _, taken := next[identity]; taken {
			continue
		}
		process, alive := processes[identity.pid]
		if !alive || process.identity != identity {
			continue
		}
		next[identity] = cpuSample{cpuSeconds: process.cpuSeconds, at: now}
	}
}

func creditableRoots(
	previous map[processIdentity]cpuSample,
	processes map[int]ownedProcess,
	roots []OwnedRoot,
	lastAt time.Time,
	now time.Time,
) map[int]struct{} {
	creditable := make(map[int]struct{}, len(roots))
	for _, root := range roots {
		process, ok := processes[root.PID]
		if !ok {
			continue
		}
		if _, based := previous[process.identity]; based {
			creditable[root.PID] = struct{}{}
			continue
		}
		// safety: with no reading for the root itself the daemon was not watching this
		// tree, so only a run that began holding since the last reading can have run
		// all its CPU inside the window. An older one stays unmeasured.
		if !root.HeldSince.IsZero() && !root.HeldSince.Before(lastAt) && !root.HeldSince.After(now) {
			creditable[root.PID] = struct{}{}
		}
	}
	return creditable
}

const processDatingSlack = 10 * time.Millisecond

func startedInWindow(startedAt, seenSince, now time.Time) bool {
	// safety: the slack is a clock tick, because a start time and an uptime each floor
	// to one and a process dated a tick early is not evidence it predates the scan.
	if seenSince.IsZero() {
		return false
	}
	return !startedAt.Before(seenSince.Add(-processDatingSlack)) && !startedAt.After(now)
}

func firstSightCredit(cpuSeconds, window, arbitratedCores float64) (float64, bool) {
	// safety: processes first seen in one tree ran all their CPU inside this window,
	// so the whole total belongs here. More than the window could hold proves some of
	// it predates the window, and nothing here can say how much, so the tree's figure
	// cannot stand. The ceiling is the capacity admission arbitrates, not the host's.
	if window <= 0 || arbitratedCores <= 0 || cpuSeconds > window*arbitratedCores {
		return 0, false
	}
	return cpuSeconds / window, true
}

type ProcUsage struct {
	Fraction      float64
	HasDescendant bool
}

type procSampler struct {
	//lint:ignore U1000 used by platform implementations
	mu   sync.Mutex
	last map[int]cpuSample
	tree map[int]map[int]struct{}
}

type cpuSample struct {
	cpuSeconds float64
	at         time.Time
	startTicks uint64
}

func processCPUFraction(previous, current cpuSample) (float64, bool) {
	if previous.startTicks == 0 || previous.startTicks != current.startTicks {
		return 0, false
	}
	wall := current.at.Sub(previous.at).Seconds()
	delta := current.cpuSeconds - previous.cpuSeconds
	if wall <= 0 || delta < 0 {
		return 0, false
	}
	return delta / wall, true
}

type darwinCPUProcess struct {
	parentPID int

	cpuSeconds float64
}

func parseDarwinCPUTime(field string) (float64, bool) {
	days := 0.0
	if dash := strings.IndexByte(field, '-'); dash >= 0 {
		parsed, err := strconv.ParseFloat(field[:dash], 64)
		if err != nil {
			return 0, false
		}
		days, field = parsed, field[dash+1:]
	}
	parts := strings.Split(field, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for _, part := range parts {
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || value < 0 {
			return 0, false
		}
		total = total*60 + value
	}
	return days*86400 + total, true
}

func parseDarwinCPUSnapshot(output string) (map[int]darwinCPUProcess, bool) {
	processes := map[int]darwinCPUProcess{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		processID, pidErr := strconv.Atoi(fields[0])
		parentPID, parentErr := strconv.Atoi(fields[1])
		cpuSeconds, cpuOK := parseDarwinCPUTime(fields[2])
		if pidErr != nil || parentErr != nil || !cpuOK || processID <= 0 || parentPID < 0 {
			continue
		}
		processes[processID] = darwinCPUProcess{parentPID: parentPID, cpuSeconds: cpuSeconds}
	}
	return processes, len(processes) > 0
}

func darwinCPUFromSnapshot(
	processes map[int]darwinCPUProcess,
	previous map[int]darwinCPUProcess,
	elapsedSeconds float64,
	roots []OwnedRoot,
	lastAt time.Time,
	now time.Time,
	machineCores float64,
	arbitratedCores float64,
) (float64, bool, map[int]float64, bool) {
	if len(processes) == 0 || len(previous) == 0 || elapsedSeconds <= 0 {
		return 0, false, nil, false
	}
	fractions := make(map[int]float64, len(processes))
	firstSeen := make(map[int]float64, len(processes))
	backwards := map[int]struct{}{}
	var host float64
	for processID, process := range processes {
		// bug: this snapshot carries no process start time, so a pid the OS
		// recycles within one interval reads as the dead process continuing.
		// Only a falling cpuSeconds catches it.
		prior, seen := previous[processID]
		if !seen {
			// safety: a process the previous snapshot did not carry ran its CPU inside
			// this window, so the host ran it too. Counting it in owned and not in host
			// would let a run's own work exceed the machine's and drive external to zero.
			if credit, ok := firstSightCredit(process.cpuSeconds, elapsedSeconds, machineCores); ok {
				firstSeen[processID] = credit
				host += credit
			}
			continue
		}
		delta := process.cpuSeconds - prior.cpuSeconds
		if delta < 0 {
			backwards[processID] = struct{}{}
			continue
		}
		fraction := delta / elapsedSeconds
		fractions[processID] = fraction
		host += fraction
	}
	host = clampCores(host, machineCores)
	if len(roots) == 0 {
		return host, true, nil, true
	}
	rootPIDs := make(map[int]struct{}, len(roots))
	creditable := make(map[int]struct{}, len(roots))
	for _, root := range roots {
		if _, ok := processes[root.PID]; !ok {
			continue
		}
		rootPIDs[root.PID] = struct{}{}
		if _, based := previous[root.PID]; based {
			creditable[root.PID] = struct{}{}
			continue
		}
		if !root.HeldSince.IsZero() && !root.HeldSince.Before(lastAt) && !root.HeldSince.After(now) {
			creditable[root.PID] = struct{}{}
		}
	}
	parentOf := make(map[int]int, len(processes))
	for processID, process := range processes {
		parentOf[processID] = process.parentPID
	}
	owners := ownersByNearestRoot(parentOf, rootPIDs)
	byRoot := make(map[int]float64, len(rootPIDs))
	unbounded := map[int]struct{}{}
	for processID := range processes {
		root, ok := owners[processID]
		if !ok {
			continue
		}
		if _, ok := creditable[root]; !ok {
			continue
		}
		if fraction, measured := fractions[processID]; measured {
			byRoot[root] += fraction
			continue
		}
		if _, ran := backwards[processID]; ran {
			// safety: a counter that ran backwards is unreadable, so the tree it sits
			// under gets no figure. Reporting zero for it subtracts nothing from
			// external, which admits against CPU this daemon never measured.
			unbounded[root] = struct{}{}
			continue
		}
		credit, sighted := firstSeen[processID]
		if _, seen := previous[processID]; !seen && !sighted {
			unbounded[root] = struct{}{}
			continue
		}
		byRoot[root] += credit
	}
	for root := range unbounded {
		delete(byRoot, root)
	}
	for root, owned := range byRoot {
		byRoot[root] = clampCores(owned, arbitratedCores)
	}
	return host, true, byRoot, true
}

func collectSubtree(root int, children map[int][]int) []int {
	var out []int
	seen := map[int]bool{}
	stack := []int{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		stack = append(stack, children[n]...)
	}
	return out
}
