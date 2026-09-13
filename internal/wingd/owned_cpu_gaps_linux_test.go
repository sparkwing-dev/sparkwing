//go:build linux

package wingd

import (
	"testing"
	"time"
)

func TestLinuxProcess_ReadsAProcessWhoseNameCarriesAClosingParen(t *testing.T) {
	process, ok := parseLinuxProcessStat("10 (odd)name) S 7 0 0 0 0 0 0 0 0 0 200 300 400 500 0 0 0 0 600")
	if !ok {
		t.Fatal("a process may name itself with a closing paren, and every field after the name is read against the last one, not the first")
	}
	if process.parentPID != 7 {
		t.Errorf("parent PID = %d, want 7: reading the name against its first closing paren shifts every later field, so the tree this process is credited under is the wrong one", process.parentPID)
	}
	if process.startTicks != 600 {
		t.Errorf("start ticks = %d, want 600: start ticks are half this process's identity, and a shifted one makes a live process read as a recycled PID", process.startTicks)
	}
	if process.selfCPUSeconds != 5 {
		t.Errorf("self CPU seconds = %v, want 5", process.selfCPUSeconds)
	}
}

func TestLinuxProcess_RefusesAStatLineEndingBeforeTheStartTicks(t *testing.T) {
	for name, line := range map[string]string{
		"truncated mid-counter": "10 (worker) S 1 0 0 0 0 0 0 0 0 0 200 300",
		"one field short":       "10 (worker) S 1 0 0 0 0 0 0 0 0 0 200 300 400 500 0 0 0 0",
	} {
		if _, ok := parseLinuxProcessStat(line); ok {
			t.Errorf("%s: a line ending before the start ticks was read as a process; want it refused, because the identity it would carry is one this parse never saw", name)
		}
	}
}

func TestLinuxOwnedProcesses_CreditsAProcessItsOwnCPUAndNotItsTreeTotal(t *testing.T) {
	now := time.Now()
	procs := map[int]linuxProc{
		10: {parentPID: 1, startTicks: 600, selfCPUSeconds: 5, cpuSeconds: 14},
	}

	processes := linuxOwnedProcesses(procs, now, 3600)

	if got := processes[10].cpuSeconds; got != 5 {
		t.Errorf("owned process CPU seconds = %v, want 5: the tree total this process carries already holds every child it reaped, and each live child is credited its own row, so crediting the tree total here counts one reaped child under every ancestor it had",
			got)
	}
	if processes[10].identity != (processIdentity{pid: 10, startTicks: 600}) {
		t.Errorf("owned process identity = %v, want pid 10 at start ticks 600", processes[10].identity)
	}
}
