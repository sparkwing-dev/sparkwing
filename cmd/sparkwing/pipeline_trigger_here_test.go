package main

import (
	"strings"
	"testing"
)

func TestParseTriggerFlags_RunnerHere(t *testing.T) {
	flags, err := parseTriggerFlags([]string{"release", "--profile", "prod", "--runner", "here", "--reason=INC-42 fleet down", "--", "--version", "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if flags.runner != "here" || flags.reason != "INC-42 fleet down" || strings.Join(flags.passthrough, " ") != "--version v1" {
		t.Fatalf("flags = %+v", flags)
	}
	for name, args := range map[string][]string{
		"no reason":     {"release", "--runner", "here"},
		"blank reason":  {"release", "--runner=here", "--reason", " "},
		"reason alone":  {"release", "--reason", "x"},
		"other runner":  {"release", "--runner", "label=prod", "--reason", "x"},
		"detach":        {"release", "--runner", "here", "--reason", "x", "--detach"},
		"working tree":  {"release", "--runner", "here", "--reason", "x", "--working-tree"},
		"missing value": {"release", "--reason", "x", "--runner"},
	} {
		if _, err := parseTriggerFlags(append(args, "--profile", "prod")); err == nil {
			t.Errorf("%s: parseTriggerFlags(%q) accepted", name, args)
		}
	}
}
