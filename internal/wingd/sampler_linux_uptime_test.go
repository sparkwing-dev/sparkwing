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

	// safety: the child is first seen by the second scan, which is the one path
	// that asks how old a process is. Without a process the previous scan never
	// listed, both a scan that dates its processes and a scan that dates none of
	// them keep the tree, and nothing here reads the uptime the scan was given.
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatalf("start a child under this test's tree: %v", err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()

	byRoot, ok := sampler.sampleOwnedFrom(time.Now, roots, cores)
	if !ok {
		t.Fatal("the second scan could not list this machine's processes")
	}
	if _, kept := byRoot[root]; !kept {
		t.Errorf("the scan dropped this tree's figure after a child appeared under it; want the tree kept: an undated child is refused first-sight credit and takes the whole tree out of the reading, so owned CPU reads short, external reads high, and admission grants fewer cores than the host has free")
	}
}
