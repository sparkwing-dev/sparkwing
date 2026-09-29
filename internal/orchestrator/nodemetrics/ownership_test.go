package nodemetrics

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procusage"
)

func TestAttachOwnerKeepsWholeTree(t *testing.T) {
	stubReaders(t, clampingCPU(), func() int64 { return 4096 })
	t.Cleanup(SetIntervalForTest(10 * time.Millisecond))
	owner, child := &captureSink{}, &captureSink{}
	detachOwner := Attach(context.Background(), owner, true)
	defer detachOwner()
	detachChild := Attach(context.Background(), child, false)
	defer detachChild()
	full, estimate := awaitSharedTick(t, owner, child)
	if full.Estimated || !estimate.Estimated {
		t.Fatalf("attribution owner=%+v child=%+v", full, estimate)
	}
	hostMilli := int64(runtime.NumCPU()) * 1000
	if full.CPUMillicores != hostMilli || full.MemoryBytes != 4096 {
		t.Errorf("owner lost tree usage: %+v", full)
	}
	if estimate.CPUMillicores != hostMilli/2 || estimate.MemoryBytes != 2048 {
		t.Errorf("shared estimate: %+v", estimate)
	}
	detachOwner()
	alone := awaitSampleAfter(t, child, time.Now())
	if !alone.Estimated {
		t.Fatal("last shared recipient became an owner")
	}
}

func TestAttachAvailabilityIsIndependent(t *testing.T) {
	for _, tc := range []struct {
		name string
		cpu  bool
		rss  int64
	}{
		{"idle", true, 0}, {"CPU unavailable", false, 4096}, {"memory unavailable", true, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubReaders(t, func() (time.Duration, bool) { return 0, tc.cpu }, func() int64 { return tc.rss })
			t.Cleanup(SetIntervalForTest(10 * time.Millisecond))
			sink := &captureSink{}
			detach := Attach(context.Background(), sink, true)
			defer detach()
			sample := awaitSampleAfter(t, sink, time.Now())
			if sample.CPUAvailable != tc.cpu || sample.MemoryAvailable != (tc.rss >= 0) {
				t.Fatalf("availability: %+v", sample)
			}
			if sample.CPUMillicores != 0 {
				t.Fatalf("idle/unknown CPU invented usage: %+v", sample)
			}
		})
	}
}

func TestAttachReapedCPUDoesNotBecomeAnIntervalSpike(t *testing.T) {
	stubReaders(t, func() (time.Duration, bool) { return 0, true }, func() int64 { return 4096 })
	previous := snapshotReader
	reads := 0
	snapshotReader = func(tree *procusage.Tree) procusage.Snapshot {
		snapshot := previous(tree)
		reads++
		for pid, process := range snapshot.Processes {
			process.ReapedAvailable = true
			if reads > 1 {
				process.ReapedCPU = time.Hour
			}
			snapshot.Processes[pid] = process
		}
		return snapshot
	}
	t.Cleanup(SetIntervalForTest(10 * time.Millisecond))
	sink := &captureSink{}
	detach := Attach(context.Background(), sink, true)
	defer detach()
	sample := awaitSampleAfter(t, sink, time.Now())
	if sample.CPUAvailable || sample.CPUMillicores != 0 {
		t.Fatalf("reaped lifetime CPU became an interval measurement: %+v", sample)
	}
	if !sample.MemoryAvailable || sample.MemoryBytes != 4096 {
		t.Fatalf("CPU gap changed independent memory: %+v", sample)
	}
}
