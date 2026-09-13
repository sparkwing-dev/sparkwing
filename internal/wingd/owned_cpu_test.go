package wingd

import (
	"math"
	"testing"
	"time"
)

func heldRoots(pids ...int) []OwnedRoot {
	roots := make([]OwnedRoot, 0, len(pids))
	for _, pid := range pids {
		roots = append(roots, OwnedRoot{PID: pid, HeldSince: time.Unix(0, 0)})
	}
	return roots
}

func TestOwnedCPU_ReapedChildIsNotCountedAgainThroughParent(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	parent := processIdentity{pid: 10, startTicks: 1000}
	child := processIdentity{pid: 11, startTicks: 1001}
	previous := map[processIdentity]cpuSample{
		parent: {cpuSeconds: 1, at: previousAt},
		child:  {cpuSeconds: 5, at: previousAt},
	}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: parent, cpuSeconds: 2},
	}
	owners := ownedProcessOwners(heldRoots(10), processes)

	byRoot, _ := ownedCPUByRoot(previous, processes, owners, heldRoots(10), now.Add(-time.Second), now.Add(-time.Second), now, 8)
	usage := sumOwnedCPU(byRoot)

	if math.Abs(usage-1) > 0.0001 {
		t.Fatalf("owned CPU = %v; want one parent core without re-counting the reaped child", usage)
	}
}

func TestOwnedCPU_PIDReuseNeedsANewBaseline(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	previous := map[processIdentity]cpuSample{
		{pid: 10, startTicks: 1000}: {cpuSeconds: 1, at: previousAt},
	}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: processIdentity{pid: 10, startTicks: 2000}, cpuSeconds: 3},
	}
	owners := ownedProcessOwners(heldRoots(10), processes)

	byRoot, _ := ownedCPUByRoot(previous, processes, owners, heldRoots(10), now.Add(-time.Second), now.Add(-time.Second), now, 8)
	usage := sumOwnedCPU(byRoot)

	if _, figure := byRoot[10]; figure || usage != 0 {
		t.Fatalf("recycled PID CPU = %v, root has a figure %v; want no figure for the new identity, which is what an absent key means",
			usage, figure)
	}
}

func TestOwnedCPU_NewChildIsCreditedBesideTheMeasuredParentDelta(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	parent := processIdentity{pid: 10, startTicks: 1000}
	previous := map[processIdentity]cpuSample{
		parent: {cpuSeconds: 1, at: previousAt},
	}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: parent, cpuSeconds: 2},
		11: {
			parentPID:  10,
			identity:   processIdentity{pid: 11, startTicks: 1001},
			cpuSeconds: 4,
			startedAt:  previousAt.Add(500 * time.Millisecond),
		},
	}
	owners := ownedProcessOwners(heldRoots(10), processes)

	byRoot, next := ownedCPUByRoot(previous, processes, owners, heldRoots(10), previousAt, previousAt, now, 8)
	usage := sumOwnedCPU(byRoot)

	if math.Abs(usage-5) > 0.0001 {
		t.Fatalf("owned CPU = %v; want the parent's measured 1 plus the new child's whole 4: a child the sampler has not seen before ran all of its CPU inside this window, so crediting it none is what charges a run's own work to the machine",
			usage)
	}
	if len(next) != 2 {
		t.Fatalf("next baselines = %d, want parent and new child", len(next))
	}
}

func TestOwnedCPU_AProcessCarryingMoreCPUThanTheWindowCouldHoldIsNotCredited(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	lastAt := now.Add(-time.Second)
	born := lastAt.Add(100 * time.Millisecond)
	cases := map[string]map[int]ownedProcess{
		"a long-running process adopted into a tree just being watched": {
			10: {parentPID: 1, identity: processIdentity{pid: 10, startTicks: 900}, cpuSeconds: 0.1, startedAt: born},
			11: {parentPID: 10, identity: processIdentity{pid: 11, startTicks: 1}, cpuSeconds: 3600, startedAt: born},
		},
		"a run reattaching after a restart, with no reading and a fresh hold": {
			10: {parentPID: 1, identity: processIdentity{pid: 10, startTicks: 7}, cpuSeconds: 900, startedAt: born},
		},
	}
	for name, processes := range cases {
		roots := []OwnedRoot{{PID: 10, HeldSince: lastAt}}
		owners := ownedProcessOwners(roots, processes)

		byRoot, _ := ownedCPUByRoot(nil, processes, owners, roots, lastAt, lastAt, now, 8)

		if _, figure := byRoot[10]; figure {
			t.Errorf("%s: owned CPU by root = %v; want no figure at all: more CPU than the window could hold cannot have run inside it whatever the process claims about its age, and crediting that total would understate external and over-admit",
				name, byRoot)
		}
	}
}

func TestOwnedCPU_AStaleParentPIDDoesNotResurrectAMissingRoot(t *testing.T) {
	processes := map[int]ownedProcess{
		9: {parentPID: 10, identity: processIdentity{pid: 9, startTicks: 400}},
	}

	owners := ownedProcessOwners(heldRoots(10), processes)

	if len(owners) != 0 {
		t.Fatalf("owners = %v; want none: pid 10 is gone from the process table, so a survivor still naming it as its parent belongs to no root this daemon holds",
			owners)
	}
}

func TestOwnedCPU_ARootWithoutItsOwnBaselineReportsNoFigure(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	child := processIdentity{pid: 8, startTicks: 100}
	previous := map[processIdentity]cpuSample{
		child: {cpuSeconds: 7, at: previousAt},
	}
	processes := map[int]ownedProcess{
		7: {parentPID: 1, identity: processIdentity{pid: 7, startTicks: 900}, cpuSeconds: 5},
		8: {parentPID: 7, identity: child, cpuSeconds: 9},
	}
	owners := ownedProcessOwners(heldRoots(7), processes)

	byRoot, next := ownedCPUByRoot(previous, processes, owners, heldRoots(7), now.Add(-time.Second), now.Add(-time.Second), now, 8)

	if _, figure := byRoot[7]; figure {
		t.Fatalf("owned CPU by root = %v; want no figure for a root that re-execed: its own process has no baseline, so the tree's sum covers only the surviving child and is silently short",
			byRoot)
	}
	if _, carried := next[processes[7].identity]; !carried {
		t.Fatal("the new root's baseline was not carried forward, so it would report no figure on the next reading either")
	}
}

func TestOwnedCPU_ACyclicParentTableTerminates(t *testing.T) {
	done := make(chan map[int]int, 1)
	go func() {
		done <- ownersByNearestRoot(map[int]int{10: 11, 11: 12, 12: 10, 13: 10}, map[int]struct{}{13: {}})
	}()
	select {
	case owners := <-done:
		if owners[13] != 13 {
			t.Fatalf("owners = %v; want the root outside the cycle still resolved", owners)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolving a cyclic parent table did not terminate: the sample loop would spin on a bad process table")
	}
}

func TestOwnedCPU_SumsEveryMeasuredProcessUnderOneRoot(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	root := processIdentity{pid: 10, startTicks: 1000}
	child := processIdentity{pid: 11, startTicks: 1001}
	previous := map[processIdentity]cpuSample{
		root:  {cpuSeconds: 1, at: previousAt},
		child: {cpuSeconds: 2, at: previousAt},
	}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: root, cpuSeconds: 2},
		11: {parentPID: 10, identity: child, cpuSeconds: 5},
	}
	owners := ownedProcessOwners(heldRoots(10), processes)

	byRoot, _ := ownedCPUByRoot(previous, processes, owners, heldRoots(10), now.Add(-time.Second), now.Add(-time.Second), now, 8)

	if math.Abs(byRoot[10]-4) > 0.0001 {
		t.Fatalf("owned CPU by root = %v; want the root's own 1 core plus its child's 3 summed into one tree, not the last one read", byRoot)
	}
}

func TestOwnedCPU_OverlappingRootsCountTheirUnionOnce(t *testing.T) {
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: processIdentity{pid: 10, startTicks: 1000}},
		11: {parentPID: 10, identity: processIdentity{pid: 11, startTicks: 1001}},
	}

	owners := ownedProcessOwners(heldRoots(10, 11), processes)

	if len(owners) != 2 {
		t.Fatalf("owned identities = %d, want the two-process union", len(owners))
	}
	if owners[processIdentity{pid: 10, startTicks: 1000}] != 10 || owners[processIdentity{pid: 11, startTicks: 1001}] != 11 {
		t.Fatalf("owners = %v; want each process charged to its nearest ancestor root, so the union is counted once", owners)
	}
}

func TestOwnedCPU_ReholdingATreeAfterAnIdleStretchChargesItNothingForTheGap(t *testing.T) {
	firstAt := time.Unix(100, 0)
	releasedAt := firstAt.Add(10 * time.Second)
	reheldAt := releasedAt.Add(time.Hour)
	identity := processIdentity{pid: 10, startTicks: 1000}
	process := func(cpuSeconds float64) map[int]ownedProcess {
		return map[int]ownedProcess{10: {
			parentPID:  1,
			identity:   identity,
			cpuSeconds: cpuSeconds,
			startedAt:  firstAt.Add(time.Second),
		}}
	}
	sampler := &ownedProcSampler{}
	held := []OwnedRoot{{PID: 10, HeldSince: firstAt.Add(-time.Hour)}}

	processes := process(50)
	_, next := ownedCPUByRoot(
		map[processIdentity]cpuSample{identity: {cpuSeconds: 40, at: firstAt}},
		processes, ownedProcessOwners(held, processes), held, firstAt, firstAt, releasedAt, 8)
	sampler.last, sampler.lastAt, sampler.seenSince = next, releasedAt, firstAt

	sampler.forgetSamples(reheldAt)

	const reheldCounter, cores = 50.0, 8.0
	reheldNow := reheldAt.Add(10 * time.Second)
	if ceiling := reheldNow.Sub(sampler.lastAt).Seconds() * cores; reheldCounter > ceiling {
		t.Fatalf("re-held counter %v is over the %v cores-seconds this window could hold, so the capacity bound refuses the credit and the date gate below decides nothing: lower the counter rather than letting this test pass for a reason it is not about",
			reheldCounter, ceiling)
	}
	processes = process(reheldCounter)
	rehold := []OwnedRoot{{PID: 10, HeldSince: reheldAt.Add(time.Second)}}
	byRoot, _ := ownedCPUByRoot(
		sampler.last, processes, ownedProcessOwners(rehold, processes), rehold,
		sampler.lastAt, sampler.seenSince, reheldNow, cores)

	if figure, reported := byRoot[10]; reported {
		t.Fatalf("re-held tree was charged %v cores; want no figure, because the daemon watched none of the hour that CPU ran in",
			figure)
	}
}

func TestOwnedCPU_ATreeThatRanNothingReportsZeroRatherThanNoReading(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	identity := processIdentity{pid: 10, startTicks: 1000}
	previous := map[processIdentity]cpuSample{identity: {cpuSeconds: 7, at: previousAt}}
	processes := map[int]ownedProcess{10: {parentPID: 1, identity: identity, cpuSeconds: 7}}
	owners := ownedProcessOwners(heldRoots(10), processes)

	byRoot, _ := ownedCPUByRoot(previous, processes, owners, heldRoots(10), previousAt, previousAt, now, 8)

	figure, reported := byRoot[10]
	if !reported || figure != 0 {
		t.Fatalf("idle tree reports %v with a figure %v; want zero cores reported, because an absent key means no reading and would charge the host for a run that ran nothing",
			figure, reported)
	}
}

func TestOwnedCPU_ReleasingOneOfTwoRootsLeavesTheOtherMeasurable(t *testing.T) {
	firstAt := time.Unix(100, 0)
	secondAt := firstAt.Add(10 * time.Second)
	thirdAt := secondAt.Add(10 * time.Second)
	kept := processIdentity{pid: 10, startTicks: 1000}
	released := processIdentity{pid: 20, startTicks: 2000}
	table := func(keptCPU, releasedCPU float64) map[int]ownedProcess {
		return map[int]ownedProcess{
			10: {parentPID: 1, identity: kept, cpuSeconds: keptCPU},
			20: {parentPID: 1, identity: released, cpuSeconds: releasedCPU},
		}
	}
	both := []OwnedRoot{
		{PID: 10, HeldSince: firstAt.Add(-time.Hour)},
		{PID: 20, HeldSince: firstAt.Add(-time.Hour)},
	}
	onlyKept := []OwnedRoot{{PID: 10, HeldSince: firstAt.Add(-time.Hour)}}

	processes := table(10, 10)
	_, afterBoth := ownedCPUByRoot(
		map[processIdentity]cpuSample{
			kept:     {cpuSeconds: 0, at: firstAt},
			released: {cpuSeconds: 0, at: firstAt},
		},
		processes, ownedProcessOwners(both, processes), both, firstAt, firstAt, secondAt, 8)

	processes = table(20, 90)
	_, afterRelease := ownedCPUByRoot(
		afterBoth, processes, ownedProcessOwners(onlyKept, processes),
		onlyKept, secondAt, secondAt, thirdAt, 8)

	processes = table(30, 90)
	rehold := []OwnedRoot{
		{PID: 10, HeldSince: firstAt.Add(-time.Hour)},
		{PID: 20, HeldSince: thirdAt.Add(time.Second)},
	}
	byRoot, _ := ownedCPUByRoot(
		afterRelease, processes, ownedProcessOwners(rehold, processes), rehold,
		thirdAt,
		thirdAt, thirdAt.Add(10*time.Second), 8)

	figure, reported := byRoot[20]
	if !reported || figure != 0 {
		t.Fatalf("re-held tree reports %v with a figure %v; want a measured zero, because the other root kept the sampler reading this tree every interval it was unheld",
			figure, reported)
	}
}

func TestOwnedCPU_ARootFirstSeenWithNoReadingBehindItGetsNoFigure(t *testing.T) {
	now := time.Unix(200, 0)
	identity := processIdentity{pid: 10, startTicks: 1000}
	processes := map[int]ownedProcess{10: {parentPID: 1, identity: identity, cpuSeconds: 305}}
	held := []OwnedRoot{{PID: 10, HeldSince: now.Add(-time.Hour)}}

	byRoot, _ := ownedCPUByRoot(
		nil, processes, ownedProcessOwners(held, processes), held, time.Time{}, time.Time{}, now, 8)

	if figure, reported := byRoot[10]; reported {
		t.Fatalf("root reported %v cores against no previous reading; want no figure, because the counter says nothing about when that CPU ran",
			figure)
	}
}

func TestOwnedProcSampler_AReadingWithNothingHeldEndsTheWindow(t *testing.T) {
	sampler := &ownedProcSampler{}
	identity := processIdentity{pid: 10, startTicks: 1000}
	firstAt := time.Now().Add(-time.Hour)
	sampler.last = map[processIdentity]cpuSample{identity: {cpuSeconds: 0, at: firstAt}}
	sampler.lastAt = firstAt

	if _, measured := sampler.CPUUsage(nil, 8); !measured {
		t.Fatal("a reading with nothing held reported the host unreadable; nothing failed to be read")
	}

	processes := map[int]ownedProcess{10: {parentPID: 1, identity: identity, cpuSeconds: 3600}}
	rehold := []OwnedRoot{{PID: 10, HeldSince: time.Now()}}
	byRoot, _ := ownedCPUByRoot(
		sampler.last, processes, ownedProcessOwners(rehold, processes), rehold,
		sampler.lastAt,
		sampler.lastAt, time.Now().Add(10*time.Second), 8)

	if figure, reported := byRoot[10]; reported {
		t.Fatalf("re-held tree was charged %v cores; want no figure, because the reading with nothing held ended the window that CPU ran in",
			figure)
	}
}

func TestOwnedCPU_ATreeWithAnUnreadableCounterReportsNoFigure(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	root := processIdentity{pid: 10, startTicks: 1000}
	child := processIdentity{pid: 11, startTicks: 1001}
	previous := map[processIdentity]cpuSample{
		root:  {cpuSeconds: 50, at: previousAt},
		child: {cpuSeconds: 0, at: previousAt},
	}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: root, cpuSeconds: 3},
		11: {parentPID: 10, identity: child, cpuSeconds: 2},
	}
	owners := ownedProcessOwners(heldRoots(10), processes)

	byRoot, _ := ownedCPUByRoot(previous, processes, owners, heldRoots(10), previousAt, previousAt, now, 8)

	if figure, reported := byRoot[10]; reported {
		t.Fatalf("tree reported %v cores with one counter unreadable; want no figure, because the rest of the tree is short by an unknown amount and a short figure still subtracts from external",
			figure)
	}
}

func TestOwnedCPU_AProcessBornWhileTheLastReadingScannedIsStillCredited(t *testing.T) {
	scanStart := time.Unix(100, 0)
	lastAt := scanStart.Add(50 * time.Millisecond)
	now := lastAt.Add(5 * time.Second)
	root := processIdentity{pid: 10, startTicks: 1000}
	child := processIdentity{pid: 11, startTicks: 1001}
	previous := map[processIdentity]cpuSample{root: {cpuSeconds: 1, at: lastAt}}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: root, cpuSeconds: 1, startedAt: scanStart.Add(-time.Hour)},
		11: {parentPID: 10, identity: child, cpuSeconds: 3, startedAt: scanStart.Add(20 * time.Millisecond)},
	}
	held := []OwnedRoot{{PID: 10, HeldSince: scanStart.Add(-time.Hour)}}

	byRoot, _ := ownedCPUByRoot(previous, processes, ownedProcessOwners(held, processes), held, lastAt, scanStart, now, 8)

	if figure, reported := byRoot[10]; !reported || math.Abs(figure-0.6) > 0.0001 {
		t.Fatalf("tree reports a figure %[2]v carrying %[1]v; want the child's three CPU-seconds over the five-second window: it started after the previous reading began listing, so that reading could not have seen it and its absence is not evidence of age",
			figure, reported)
	}
}

func TestOwnedCPU_AProcessDatedOneTickBeforeTheScanKeepsItsCredit(t *testing.T) {
	scanStart := time.Unix(100, 0)
	lastAt := scanStart.Add(50 * time.Millisecond)
	now := lastAt.Add(5 * time.Second)
	root := processIdentity{pid: 10, startTicks: 1000}
	child := processIdentity{pid: 11, startTicks: 1001}
	previous := map[processIdentity]cpuSample{root: {cpuSeconds: 1, at: lastAt}}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: root, cpuSeconds: 1, startedAt: scanStart.Add(-time.Hour)},
		11: {
			parentPID:  10,
			identity:   child,
			cpuSeconds: 3,
			startedAt:  scanStart.Add(-5 * time.Millisecond),
		},
	}
	held := []OwnedRoot{{PID: 10, HeldSince: scanStart.Add(-time.Hour)}}

	byRoot, _ := ownedCPUByRoot(previous, processes, ownedProcessOwners(held, processes), held, lastAt, scanStart, now, 8)

	if figure, reported := byRoot[10]; !reported || math.Abs(figure-0.6) > 0.0001 {
		t.Fatalf("tree reports a figure %[2]v carrying %[1]v; want the child's three CPU-seconds over the five-second window: a tick of dating resolution is not evidence the process predates the scan, and refusing it costs the whole tree its figure",
			figure, reported)
	}
}

func TestStartedInWindow_AdmitsOnlyAProcessThePreviousScanCouldNotHaveSeen(t *testing.T) {
	scanStart := time.Unix(100, 0)
	now := scanStart.Add(5 * time.Second)
	for name, tc := range map[string]struct {
		startedAt time.Time
		seenSince time.Time
		want      bool
	}{
		"undatable process":         {time.Time{}, scanStart, false},
		"no previous scan":          {scanStart.Add(time.Second), time.Time{}, false},
		"running before the scan":   {scanStart.Add(-time.Hour), scanStart, false},
		"a tick before the scan":    {scanStart.Add(-5 * time.Millisecond), scanStart, true},
		"two ticks before the scan": {scanStart.Add(-20 * time.Millisecond), scanStart, false},
		// safety: these two pin the slack to one clock tick from both sides. A
		// bound expressed against the constant moves with it and admits any value.
		"exactly one clock tick before the scan": {scanStart.Add(-10 * time.Millisecond), scanStart, true},
		"a nanosecond older than one clock tick": {scanStart.Add(-10*time.Millisecond - time.Nanosecond), scanStart, false},
		"born during the scan":                   {scanStart.Add(time.Millisecond), scanStart, true},
		"born inside the window":                 {now.Add(-time.Second), scanStart, true},
		"dated after this reading":               {now.Add(time.Hour), scanStart, false},
	} {
		if got := startedInWindow(tc.startedAt, tc.seenSince, now); got != tc.want {
			t.Errorf("%s: startedInWindow = %v, want %v", name, got, tc.want)
		}
	}
}

func TestParseProcUptime_RefusesAnythingItCannotRead(t *testing.T) {
	if got, ok := parseProcUptime("343083.69 2724178.71\n"); !ok || got != 343083.69 {
		t.Errorf("parseProcUptime = %v, %v; want the first field", got, ok)
	}
	if got, ok := parseProcUptime("12.5"); !ok || got != 12.5 {
		t.Errorf("parseProcUptime with one field = %v, %v; want it read", got, ok)
	}
	for name, data := range map[string]string{
		"empty":        "",
		"blank":        "   \n",
		"not a number": "unknown 1\n",
		"zero":         "0 0\n",
		"negative":     "-1 0\n",
		// safety: ParseFloat reads each of these without reporting an error, and
		// a bound written as a comparison admits a NaN.
		"the IEEE not-a-number": "nan 0\n",
		"positive infinity":     "inf 0\n",
		"spelled-out infinity":  "Infinity 0\n",
	} {
		if got, ok := parseProcUptime(data); ok || got != 0 {
			t.Errorf("%s: parseProcUptime = %v, %v; want refused, because a bad uptime dates every process wrongly rather than leaving it undated",
				name, got, ok)
		}
	}
}

func TestOwnedCPU_ATreeWithAnUndatableProcessReportsNoFigure(t *testing.T) {
	scanStart := time.Unix(100, 0)
	lastAt := scanStart.Add(50 * time.Millisecond)
	now := lastAt.Add(5 * time.Second)
	root := processIdentity{pid: 10, startTicks: 1000}
	previous := map[processIdentity]cpuSample{root: {cpuSeconds: 1, at: lastAt}}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: root, cpuSeconds: 3, startedAt: scanStart.Add(-time.Hour)},
		11: {parentPID: 10, identity: processIdentity{pid: 11, startTicks: 1001}, cpuSeconds: 2},
	}
	held := []OwnedRoot{{PID: 10, HeldSince: scanStart.Add(-time.Hour)}}

	byRoot, _ := ownedCPUByRoot(previous, processes, ownedProcessOwners(held, processes), held, lastAt, scanStart, now, 8)

	if figure, reported := byRoot[10]; reported {
		t.Fatalf("tree reported %v cores beside a process it could not date; want no figure, because the rest of the tree is short by an unknown amount and a short figure still subtracts from external",
			figure)
	}
}

func TestOwnedProcSampler_ASecondScanDatesAProcessFromWhenTheFirstBeganListing(t *testing.T) {
	firstScanStart := time.Unix(100, 0)
	firstNow := firstScanStart.Add(50 * time.Millisecond)
	secondNow := firstNow.Add(5 * time.Second)
	root := processIdentity{pid: 10, startTicks: 1000}
	child := processIdentity{pid: 11, startTicks: 1001}
	held := []OwnedRoot{{PID: 10, HeldSince: firstScanStart.Add(-time.Hour)}}
	oldRoot := ownedProcess{parentPID: 1, identity: root, cpuSeconds: 1, startedAt: firstScanStart.Add(-time.Hour)}

	sampler := &ownedProcSampler{}
	sampler.creditScan(map[int]ownedProcess{10: oldRoot}, held, scanWindow{startedListingAt: firstScanStart, readAt: firstNow}, 8)

	byRoot := sampler.creditScan(map[int]ownedProcess{
		10: oldRoot,
		11: {parentPID: 10, identity: child, cpuSeconds: 3, startedAt: firstScanStart.Add(20 * time.Millisecond)},
	}, held, scanWindow{startedListingAt: firstNow, readAt: secondNow}, 8)

	if figure, reported := byRoot[10]; !reported || math.Abs(figure-0.6) > 0.0001 {
		t.Fatalf("tree reports a figure %[2]v carrying %[1]v; want the child's three CPU-seconds over the five-second window: the scan it is missing from was already listing when the child began, so its absence there is not evidence of age",
			figure, reported)
	}
}

func TestOwnedProcSampler_ReHoldingAfterAnIdleStretchDatesFromWhenItStoppedWatching(t *testing.T) {
	idleAt := time.Unix(100, 0)
	scanStart := idleAt.Add(2 * time.Second)
	now := scanStart.Add(50 * time.Millisecond)
	root := processIdentity{pid: 10, startTicks: 1000}
	held := []OwnedRoot{{PID: 10, HeldSince: idleAt.Add(time.Second)}}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: root, cpuSeconds: 3, startedAt: idleAt.Add(time.Second)},
	}

	sampler := &ownedProcSampler{}
	sampler.forgetSamples(idleAt)

	byRoot := sampler.creditScan(processes, held, scanWindow{startedListingAt: scanStart, readAt: now}, 8)

	if figure, reported := byRoot[10]; !reported || math.Abs(figure-3.0/2.05) > 0.0001 {
		t.Fatalf("tree reports a figure %[2]v carrying %[1]v; want its three CPU-seconds over the 2.05-second window: the sampler stopped watching at a known instant, and a run begun after that instant ran every one of those seconds inside the window",
			figure, reported)
	}
}

func TestProcessStartFromCreation_KeepsTheMonotonicReadingTheScanBoundsCarry(t *testing.T) {
	now := time.Now()
	createdAt := now.Add(-5 * time.Second).Round(0)

	started := processStartFromCreation(now, createdAt)

	if started.Round(0) != createdAt {
		t.Fatalf("process dated %v; want the creation stamp %v, because the age is what the platform reported",
			started.Round(0), createdAt)
	}
	if started == started.Round(0) {
		t.Fatalf("process start %v carries no monotonic reading; want one, because a comparison against the scan bounds then falls back to the wall clock and a step larger than the dating slack moves the admit bound",
			started)
	}
}

func TestProcessStartFromCreation_RefusesAProcessCreatedAfterTheScan(t *testing.T) {
	now := time.Now()

	started := processStartFromCreation(now, now.Add(time.Second).Round(0))

	if !started.IsZero() {
		t.Fatalf("process dated %v; want no date, because a creation stamp after the scan is a clock the daemon cannot measure against",
			started)
	}
}

func TestFirstSightCredit_BoundsAFirstSeenTotalByTheArbitratedCapacity(t *testing.T) {
	for name, tc := range map[string]struct {
		cpuSeconds, window, arbitratedCores, want float64
		wantOK                                    bool
	}{
		"a total the arbitrated capacity could just have run": {8, 2, 4, 4, true},
		"more than the arbitrated capacity could have run":    {8.5, 2, 4, 0, false},
		"that same total under twice the capacity":            {8.5, 2, 8, 4.25, true},
		"a window that stood still":                           {1, 0, 4, 0, false},
		"no arbitrated capacity to bound it against":          {0, 2, 0, 0, false},
	} {
		got, ok := firstSightCredit(tc.cpuSeconds, tc.window, tc.arbitratedCores)
		if ok != tc.wantOK || math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s: firstSightCredit(%v, %v, %v) = %v, %v; want %v, %v: the ceiling is the capacity admission arbitrates, and a looser one credits CPU that ran before the window while a tighter one drops a figure the tree earned",
				name, tc.cpuSeconds, tc.window, tc.arbitratedCores, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestProcessStartFromUptime_KeepsTheMonotonicReadingTheScanBoundsCarry(t *testing.T) {
	now := time.Now()

	started := processStartFromUptime(now, 3600, 60)

	if started == started.Round(0) {
		t.Fatalf("process start %v carries no monotonic reading; want one, because a comparison against the scan bounds then falls back to the wall clock and a step larger than the dating slack moves the admit bound",
			started)
	}
	if got, want := now.Sub(started), 3540*time.Second; got != want {
		t.Fatalf("process aged %v off the boot clock, want %v", got, want)
	}
}

func TestProcessStartFromUptime_RefusesADateItCannotStandBehind(t *testing.T) {
	now := time.Now()
	for name, tc := range map[string]struct{ uptimeSeconds, startSeconds float64 }{
		"unreadable uptime":          {0, 0},
		"negative uptime":            {-1, 0},
		"a start after the boot ran": {10, 3600},
		// safety: a NaN age converts to a zero Duration, so the process dates to
		// the instant of the scan -- inside every window, and carrying a monotonic
		// reading, so asking the result whether it kept one answers yes.
		"an uptime that is not a number": {math.NaN(), 60},
	} {
		if got := processStartFromUptime(now, tc.uptimeSeconds, tc.startSeconds); !got.IsZero() {
			t.Errorf("%s: process start = %v; want undated, so the tree goes unmeasured rather than credited on a bad date", name, got)
		}
	}
}

func TestOwnedCPU_ARecycledPIDDoesNotInheritTheDeadProcessBaseline(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	lastAt := now.Add(-time.Second)
	dead := processIdentity{pid: 10, startTicks: 1}
	previous := map[processIdentity]cpuSample{dead: {cpuSeconds: 5, at: lastAt}}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: processIdentity{pid: 10, startTicks: 2}, cpuSeconds: 99, startedAt: lastAt},
	}
	held := []OwnedRoot{{PID: 4242, HeldSince: lastAt}}

	_, next := ownedCPUByRoot(previous, processes, ownedProcessOwners(held, processes), held, lastAt, lastAt, now, 8)

	if sample, carried := next[dead]; carried {
		t.Fatalf("the dead identity carried forward %v CPU-seconds read off the process that reused its PID; want it dropped, because re-holding that tree would then charge the window a delta measured against a counter belonging to neither process",
			sample.cpuSeconds)
	}
}

func TestProcessStartFromCreation_RefusesACreationStampTooOldToKeepAMonotonicReading(t *testing.T) {
	now := time.Now()
	for name, createdAt := range map[string]time.Time{
		// a creation stamp a garbage FILETIME reaches: the conversion clamps to
		// 1677 at the low end, so every one of these is a value the caller can
		// hand over, not a value only a test can build.
		"past the range a duration holds":      time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC),
		"inside the range a duration holds":    time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC),
		"an age no source could have measured": {},
	} {
		if started := processStartFromCreation(now, createdAt); !started.IsZero() {
			t.Fatalf("%s: process dated %v; want no date, because subtracting an age this wide lands outside the range a monotonic reading survives and the result silently carries none",
				name, started)
		}
	}
}

func TestProcessStartFromUptime_RefusesAnUptimeTooWideToKeepAMonotonicReading(t *testing.T) {
	const secondsIn200Years = 200 * 365 * 24 * 60 * 60

	started := processStartFromUptime(time.Now(), secondsIn200Years, 0)

	if !started.IsZero() {
		t.Fatalf("process dated %v; want no date: no machine reports an uptime this wide, and the two dating paths answer the same way so neither grows a case the other lacks",
			started)
	}
}
