package opsview_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// TestBudgetNote_NamesTheSetting checks the queue's budget row names the
// setting the budget came from, for each source an operator can act on.
// A cap they can see but not locate is one they cannot revoke.
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
				Origin: "/home/op/.config/sparkwing/budget",
			},
			expect: []string{"5.0 cores (machine 10.0)", "/home/op/.config/sparkwing/budget"},
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
				Origin:         "/home/op/.config/sparkwing/budget",
			},
			expect: []string{"no capacity cap", "/home/op/.config/sparkwing/budget"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := opsview.BudgetNote(&tc.state)
			for _, want := range tc.expect {
				if !strings.Contains(got, want) {
					t.Errorf("budget note %q does not contain %q", got, want)
				}
			}
		})
	}
}

// TestBudgetNote_UnsetSaysSo is the negative control in the view. With no
// budget set anywhere, the queue must state that rather than print a
// number that reads like a deliberate whole-machine choice, or say
// nothing at all. Silence and "capped at the machine total" are what an
// operator hunting a phantom cap cannot tell apart from a real one.
func TestBudgetNote_UnsetSaysSo(t *testing.T) {
	note := opsview.BudgetNote(&wingwire.BudgetState{
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

// TestBudgetNote_UnreportedBudgetClaimsNothing checks the view stays
// silent when the daemon did not describe its budget -- an older build,
// or a controller's queue view. "Not reported" is not evidence of "not
// set", and printing the stronger claim would invent an all-clear.
func TestBudgetNote_UnreportedBudgetClaimsNothing(t *testing.T) {
	if note := opsview.BudgetNote(nil); note != "" {
		t.Errorf("budget note = %q, want empty when the daemon reported no budget state", note)
	}
	old := wingwire.BudgetState{Cores: 5, MachineCores: 10}
	if note := opsview.BudgetNote(&old); !strings.Contains(note, "5.0 cores") {
		t.Errorf("budget note = %q, want an older daemon's caps still rendered", note)
	} else if strings.Contains(note, "none set") {
		t.Errorf("budget note = %q, want no claim about a source the daemon did not send", note)
	}
}

// TestExternalIgnoredNote_NamesTheSetting checks the escape hatch says
// where it was turned on. ignore-external makes the machine admit against
// total capacity while real external load goes unsubtracted, and an
// operator who finds it on has no other way to learn who set it.
func TestExternalIgnoredNote_NamesTheSetting(t *testing.T) {
	qs := wingwire.QueueState{
		IgnoreExternal: true,
		Budget: &wingwire.BudgetState{
			Cores: 10, MachineCores: 10, IgnoreExternal: true,
			Source: string(wingwire.BudgetSourceConfig),
			Origin: "/home/op/.config/sparkwing/budget",
		},
	}
	note := opsview.ExternalIgnoredNote(qs)
	if !strings.Contains(note, "/home/op/.config/sparkwing/budget") {
		t.Errorf("external note = %q, want it to name the setting that turned it on", note)
	}

	var buf bytes.Buffer
	if err := opsview.RenderQueuePretty(&buf, qs); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	if !strings.Contains(buf.String(), "/home/op/.config/sparkwing/budget") {
		t.Errorf("queue view does not name the setting behind ignore-external:\n%s", buf.String())
	}
}

// TestRenderDoctorPretty_ReportsMachineBudget checks a non-default budget
// surfaces in doctor on the healthy path. A machine admitting against
// total capacity while ignoring real external load is a state someone
// should be able to discover without already suspecting it.
func TestRenderDoctorPretty_ReportsMachineBudget(t *testing.T) {
	r := opsview.DoctorReport{
		MachineBudget: &opsview.DoctorMachineBudget{
			Source:         string(wingwire.BudgetSourceConfig),
			Origin:         "/home/op/.config/sparkwing/budget",
			Raw:            "ignore-external",
			IgnoreExternal: true,
		},
	}
	if !r.Clean() {
		t.Fatal("a machine budget made the sweep unclean; a setting is not a fault")
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "pretty", ""); err != nil {
		t.Fatalf("render doctor: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"machine budget", "/home/op/.config/sparkwing/budget", "external load ignored"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output does not contain %q:\n%s", want, out)
		}
	}
}

// TestRenderDoctorPretty_EnvBudgetWarnsItDies checks doctor says out loud
// that an env-sourced budget will not survive the next respawn, because
// the daemon is spawned by whichever gate runs first and inherits that
// process's environment.
func TestRenderDoctorPretty_EnvBudgetWarnsItDies(t *testing.T) {
	r := opsview.DoctorReport{
		MachineBudget: &opsview.DoctorMachineBudget{
			Source: string(wingwire.BudgetSourceEnv),
			Origin: "SPARKWING_BUDGET",
			Raw:    "ignore-external",
		},
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "pretty", ""); err != nil {
		t.Fatalf("render doctor: %v", err)
	}
	if !strings.Contains(buf.String(), "dies with the daemon") {
		t.Errorf("doctor output does not warn that an env budget is not durable:\n%s", buf.String())
	}
}

// TestRenderDoctorPlain_StatesBudgetEitherWay checks the machine-readable
// doctor view always carries a budget line, so "unset" is an answer the
// output gives rather than one a reader infers from a missing row.
func TestRenderDoctorPlain_StatesBudgetEitherWay(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, opsview.DoctorReport{}, "plain", ""); err != nil {
		t.Fatalf("render doctor: %v", err)
	}
	if !strings.Contains(buf.String(), "machine_budget\tunset") {
		t.Errorf("plain doctor output does not state an unset budget:\n%s", buf.String())
	}

	buf.Reset()
	r := opsview.DoctorReport{
		MachineBudget: &opsview.DoctorMachineBudget{
			Source:         string(wingwire.BudgetSourceConfig),
			Origin:         "/home/op/.config/sparkwing/budget",
			IgnoreExternal: true,
		},
	}
	if err := opsview.RenderDoctor(&buf, r, "plain", ""); err != nil {
		t.Fatalf("render doctor: %v", err)
	}
	if !strings.Contains(buf.String(), "machine_budget\tconfig\t/home/op/.config/sparkwing/budget\t1") {
		t.Errorf("plain doctor output does not carry the budget source and origin:\n%s", buf.String())
	}
}
