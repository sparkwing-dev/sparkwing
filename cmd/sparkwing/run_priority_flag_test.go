package main

import (
	"slices"
	"strings"
	"testing"
)

func TestParseRunFlags_Priority(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--sw-priority", "12"}, "12"},
		{"equals-form", []string{"--sw-priority=12"}, "12"},
		{"front", []string{"--sw-priority", "front"}, "front"},
		{"back-equals-form", []string{"--sw-priority=back"}, "back"},
		{"negative", []string{"--sw-priority", "-3"}, "-3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, pass := parseRunFlags(tc.args)
			if !wf.prioritySet {
				t.Fatalf("prioritySet = false for %v", tc.args)
			}
			if wf.priority != tc.want {
				t.Errorf("priority = %q, want %q", wf.priority, tc.want)
			}
			if len(pass) != 0 {
				t.Errorf("passthrough should be empty, got %v", pass)
			}
		})
	}
}

// safety: a bad value must reach validation rather than the pipeline, or the
// run would queue at the author's priority while reporting nothing.
func TestParseRunFlags_PriorityIsNeverForwarded(t *testing.T) {
	for _, args := range [][]string{
		{"--sw-priority", "sideways"},
		{"--sw-priority=sideways"},
		{"--sw-priority"},
	} {
		wf, pass := parseRunFlags(args)
		if !wf.prioritySet {
			t.Errorf("%v: prioritySet = false", args)
		}
		if slices.Contains(pass, "--sw-priority") {
			t.Errorf("%v: --sw-priority reached the pipeline args %v", args, pass)
		}
	}
}

func TestValidatePriorityFlag(t *testing.T) {
	for _, in := range []string{"0", "7", "-7", "front", "back", " 5 "} {
		got, err := validatePriorityFlag(in)
		if err != nil {
			t.Errorf("validatePriorityFlag(%q) = %v", in, err)
		}
		if got != strings.TrimSpace(in) {
			t.Errorf("validatePriorityFlag(%q) = %q", in, got)
		}
	}
	for _, in := range []string{"", "sideways", "3.5", "high", "front-most"} {
		got, err := validatePriorityFlag(in)
		if err == nil {
			t.Errorf("validatePriorityFlag(%q) = %q, want an error", in, got)
			continue
		}
		for _, want := range []string{"--sw-priority", "front", "back"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("validatePriorityFlag(%q) error %q does not name %q", in, err, want)
			}
		}
	}
}

func TestRunsSubmitDoesNotRefusePriority(t *testing.T) {
	if _, refused := undetachableFlags["--sw-priority"]; refused {
		t.Fatal("--sw-priority is carried on the trigger; it must not be in undetachableFlags")
	}
	if err := refuseUndetachableFlags([]string{"--sw-priority", "front"}); err != nil {
		t.Fatalf("refuseUndetachableFlags = %v", err)
	}
}
