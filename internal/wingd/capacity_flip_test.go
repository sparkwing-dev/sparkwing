package wingd_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// cgroupFixture lays a minimal cgroup v2 tree under a fresh root with the
// given cpu.max body, and returns the root plus the cpu.max path so a test can
// rewrite the quota mid-run to simulate an instance resize.
func cgroupFixture(t *testing.T, cpuMaxBody string) (root, cpuMaxPath string) {
	t.Helper()
	root = t.TempDir()
	writeFixture(t, filepath.Join(root, "proc", "self", "cgroup"), "0::/\n")
	cpuMaxPath = filepath.Join(root, "sys", "fs", "cgroup", "cpu.max")
	writeFixture(t, cpuMaxPath, cpuMaxBody)
	return root, cpuMaxPath
}

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func coresCapacity(qs wingwire.QueueState) float64 {
	for _, r := range qs.Resources {
		if r.Key == "cores" {
			return r.Capacity
		}
	}
	return -1
}

func queueOnce(t *testing.T, home string) wingwire.QueueState {
	t.Helper()
	q := ensure(t, home, "")
	qs, err := q.QueueState(context.Background())
	_ = q.Close()
	if err != nil {
		t.Fatalf("queue state: %v", err)
	}
	return qs
}

func waitForCapacity(t *testing.T, home string, want float64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if coresCapacity(queueOnce(t, home)) == want {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("cores capacity never reached %v (last %v)", want, coresCapacity(queueOnce(t, home)))
}

// TestCapacity_CgroupGrowFlipIsPickedUp proves capacity is a living value: a
// daemon started under a 4-core cgroup quota re-derives its capacity when the
// quota is raised to 8 cores mid-run, without a restart, and records the shift
// for the queue header.
func TestCapacity_CgroupGrowFlipIsPickedUp(t *testing.T) {
	root, cpuMax := cgroupFixture(t, "400000 100000")
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(8, 8<<30),
		ContainerRoot:    root,
		HeadroomFraction: -1,
		SampleInterval:   15 * time.Millisecond,
		CapacityInterval: 15 * time.Millisecond,
	})

	waitForCapacity(t, home, 4)

	writeFixture(t, cpuMax, "800000 100000")
	waitForCapacity(t, home, 8)

	qs := queueOnce(t, home)
	if qs.CapacityChange == nil {
		t.Fatal("capacity grew but the queue header carries no capacity-change note")
	}
	if qs.CapacityChange.FromCores != 4 || qs.CapacityChange.ToCores != 8 {
		t.Fatalf("capacity change = %+v, want 4 -> 8", qs.CapacityChange)
	}
}

// TestCapacity_ShrinkNeverEvictsHolder proves a mid-run shrink drains rather
// than evicts: when the cgroup quota drops below what a holder already holds,
// the holder keeps its lease and the ledger total is floored at the granted
// amount instead of resizing under the running work.
func TestCapacity_ShrinkNeverEvictsHolder(t *testing.T) {
	root, cpuMax := cgroupFixture(t, "800000 100000")
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(8, 8<<30),
		ContainerRoot:    root,
		HeadroomFraction: -1,
		SampleInterval:   15 * time.Millisecond,
		CapacityInterval: 15 * time.Millisecond,
	})
	waitForCapacity(t, home, 8)

	holder := ensure(t, home, "")
	if _, err := holder.Acquire(context.Background(), coreReq("big-holder", 6), nil); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	writeFixture(t, cpuMax, "200000 100000")
	waitForCapacity(t, home, 6)

	qs := queueOnce(t, home)
	found := false
	for _, h := range qs.Holders {
		if h.RunID == "big-holder" {
			found = true
		}
	}
	if !found {
		t.Fatal("shrink evicted the running holder; it should have drained naturally")
	}
}
