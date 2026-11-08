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
	now := time.Now()
	clock := func() time.Time { return now }
	roots := []OwnedRoot{{PID: root, HeldSince: now}}
	cores := float64(runtime.NumCPU())
	sampler := newOwnedCPUSampler()

	if _, ok := sampler.sampleOwnedFrom(clock, roots, cores); !ok {
		t.Fatal("the scan could not list this machine's processes, so it never reached the dating this test is about")
	}

	// safety: the child is first seen by the second scan, which is the one path that
	// asks a process's age. Without it, a scan dating nothing still keeps the tree.
	input, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer release.Close()
	child := exec.Command("cat")
	child.Stdin = input
	if err := child.Start(); err != nil {
		t.Fatalf("start a child under this test's tree: %v", err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()

	process, ok := linuxProcess(child.Process.Pid)
	if !ok {
		t.Fatal("the child has no process identity")
	}
	identity := processIdentity{pid: child.Process.Pid, startTicks: process.startTicks}

	// safety: the injected window keeps this process's unrelated CPU from deciding
	// the capacity bound before the child's start date is checked.
	now = now.Add(time.Hour)

	byRoot, ok := sampler.sampleOwnedFrom(clock, roots, cores)
	if !ok {
		t.Fatal("the second scan could not list this machine's processes")
	}
	if _, sampled := sampler.last[identity]; !sampled {
		t.Fatal("the second scan did not see the new child")
	}
	if _, kept := byRoot[root]; !kept {
		t.Errorf("the scan dropped this tree's figure after a child appeared under it; want the tree kept: an undated child is refused first-sight credit and takes the whole tree out of the reading, so owned CPU reads short, external reads high, and admission grants fewer cores than the host has free")
	}
}
