package opsview

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestBudgetNote_NamesTheSetting(t *testing.T) {
	tests := []struct {
		name   string
		state  wingwire.BudgetState
		expect []string
	}{
		{
			name: "config file",
			state: wingwire.BudgetState{
				Cores: 5, MachineCores: 10,
				Source: string(wingwire.BudgetSourceConfig),
				Origin: "/home/op/.config/sparkwing/config.yaml admission.budget",
			},
			expect: []string{"5.0 cores (machine 10.0)", "/home/op/.config/sparkwing/config.yaml admission.budget"},
		},
		{
			name: "environment",
			state: wingwire.BudgetState{
				Cores: 5, MachineCores: 10,
				Source: string(wingwire.BudgetSourceEnv),
				Origin: "SPARKWING_BUDGET",
			},
			expect: []string{"SPARKWING_BUDGET"},
		},
		{
			name: "flag",
			state: wingwire.BudgetState{
				Cores: 5, MachineCores: 10,
				Source: string(wingwire.BudgetSourceFlag),
				Origin: "--budget",
			},
			expect: []string{"--budget"},
		},
		{
			name: "unrecorded source is admitted, not guessed",
			state: wingwire.BudgetState{
				Cores: 5, MachineCores: 10,
				Source: string(wingwire.BudgetSourceUnknown),
			},
			expect: []string{"unrecorded"},
		},
		{
			name: "no capacity cap still names its source",
			state: wingwire.BudgetState{
				Cores: 10, MachineCores: 10,
				IgnoreExternal: true,
				Source:         string(wingwire.BudgetSourceConfig),
				Origin:         "/home/op/.config/sparkwing/config.yaml admission.budget",
			},
			expect: []string{"no capacity cap", "/home/op/.config/sparkwing/config.yaml admission.budget"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BudgetNote(&tc.state)
			for _, want := range tc.expect {
				if !strings.Contains(got, want) {
					t.Errorf("budget note %q does not contain %q", got, want)
				}
			}
		})
	}
}

func TestBudgetNote_UnsetSaysSo(t *testing.T) {
	note := BudgetNote(&wingwire.BudgetState{
		Cores: 10, MachineCores: 10,
		MemoryBytes: 1 << 30, MachineMemoryBytes: 1 << 30,
		Source: string(wingwire.BudgetSourceUnset),
	})
	if note == "" {
		t.Fatal("no budget row with nothing set; an unbudgeted machine must say so")
	}
	if !strings.Contains(note, "none set") {
		t.Errorf("budget note = %q, want it to state that no budget is set", note)
	}
}

func TestBudgetNote_UnreportedBudgetClaimsNothing(t *testing.T) {
	if note := BudgetNote(nil); note != "" {
		t.Errorf("budget note = %q, want empty when the daemon reported no budget state", note)
	}
	old := wingwire.BudgetState{Cores: 5, MachineCores: 10}
	if note := BudgetNote(&old); !strings.Contains(note, "5.0 cores") {
		t.Errorf("budget note = %q, want an older daemon's caps still rendered", note)
	} else if strings.Contains(note, "none set") {
		t.Errorf("budget note = %q, want no claim about a source the daemon did not send", note)
	}
}

func TestExternalIgnoredNote_NamesTheSetting(t *testing.T) {
	qs := wingwire.QueueState{
		IgnoreExternal: true,
		Budget: &wingwire.BudgetState{
			Cores: 10, MachineCores: 10, IgnoreExternal: true,
			Source: string(wingwire.BudgetSourceConfig),
			Origin: "/home/op/.config/sparkwing/config.yaml admission.budget",
		},
	}
	note := externalIgnoredNote(qs)
	if !strings.Contains(note, "/home/op/.config/sparkwing/config.yaml admission.budget") {
		t.Errorf("external note = %q, want it to name the setting that turned it on", note)
	}

	var buf bytes.Buffer
	if err := RenderQueue(&buf, qs, "pretty"); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	if !strings.Contains(buf.String(), "/home/op/.config/sparkwing/config.yaml admission.budget") {
		t.Errorf("queue view does not name the setting behind ignore-external:\n%s", buf.String())
	}
}
