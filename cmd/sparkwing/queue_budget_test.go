package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestBudgetNote_ShowsCapAgainstMachine(t *testing.T) {
	note := budgetNote(&wingwire.BudgetState{Cores: 6, MachineCores: 10})
	if !strings.Contains(note, "budget 6.0 cores (machine 10.0)") {
		t.Fatalf("budget note = %q, want it to show the cap against the machine total", note)
	}
	if strings.Contains(note, "memory") {
		t.Fatalf("uncapped memory should not appear: %q", note)
	}
}

func TestBudgetNote_EnforcedAndMemory(t *testing.T) {
	note := budgetNote(&wingwire.BudgetState{
		Cores: 4, MachineCores: 8,
		MemoryBytes: 8 << 30, MachineMemoryBytes: 16 << 30,
		Enforce: true,
	})
	for _, want := range []string{"4.0 cores (machine 8.0)", "memory", "OS-enforced"} {
		if !strings.Contains(note, want) {
			t.Errorf("budget note %q missing %q", note, want)
		}
	}
}

func TestBudgetNote_NilAndUncapped(t *testing.T) {
	if budgetNote(nil) != "" {
		t.Error("nil budget must render no note")
	}
	if got := budgetNote(&wingwire.BudgetState{Cores: 10, MachineCores: 10}); got != "" {
		t.Errorf("a budget equal to the machine renders no note, got %q", got)
	}
}

func TestContainerNote_ShowsLimitAgainstHost(t *testing.T) {
	note := containerNote(&wingwire.ContainerLimit{
		Cores: 6, HostCores: 24,
		MemoryBytes: 6 << 30, HostMemoryBytes: 24 << 30,
	})
	for _, want := range []string{"container limit:", "6.0 cores (host 24.0)", "6.0 GiB memory (host 24.0 GiB)"} {
		if !strings.Contains(note, want) {
			t.Errorf("container note %q missing %q", note, want)
		}
	}
}

func TestContainerNote_NilAndMemoryOnly(t *testing.T) {
	if containerNote(nil) != "" {
		t.Error("nil container limit must render no note")
	}
	note := containerNote(&wingwire.ContainerLimit{MemoryBytes: 2 << 30, HostMemoryBytes: 16 << 30})
	if !strings.Contains(note, "memory") || strings.Contains(note, "cores") {
		t.Errorf("memory-only container note = %q, want memory without cores", note)
	}
}
