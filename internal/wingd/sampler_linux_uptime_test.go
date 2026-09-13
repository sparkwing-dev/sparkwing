//go:build linux

package wingd

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestLinuxSampleOwned_KeepsATreeWhoseNewChildTheScanCanDate(t *testing.T) {
	root := os.Getpid()
	roots := []OwnedRoot{{PID: root, HeldSince: time.Now()}}
	cores := float64(runtime.NumCPU())
	sampler := newOwnedCPUSampler()

	if _, ok := sampler.sampleOwnedFrom(time.Now, roots, cores); !ok {
		t.Fatal("the scan could not list this machine's processes, so it never reached the dating this test is about")
	}

	// safety: the child is first seen by the second scan, which is the one path that
	// asks a process's age. Without it, a scan dating nothing still keeps the tree.
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatalf("start a child under this test's tree: %v", err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()

	// safety: a sparse /proc scans in microseconds, and one tick of child CPU exceeds
	// that window's ceiling, so the capacity bound would decide here instead.
	time.Sleep(50 * time.Millisecond)

	byRoot, ok := sampler.sampleOwnedFrom(time.Now, roots, cores)
	if !ok {
		t.Fatal("the second scan could not list this machine's processes")
	}
	if _, kept := byRoot[root]; !kept {
		t.Errorf("the scan dropped this tree's figure after a child appeared under it; want the tree kept: an undated child is refused first-sight credit and takes the whole tree out of the reading, so owned CPU reads short, external reads high, and admission grants fewer cores than the host has free")
	}
}
