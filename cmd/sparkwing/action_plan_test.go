package main

import (
	"slices"
	"testing"
)

func TestParsePipelinePlanArgs_OwnsDocumentedRangeFlags(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		wantStart       string
		wantStop        string
		wantPassthrough []string
	}{
		{
			name:      "split values",
			args:      []string{"--name", "gate", "--start-at", "test", "--stop-at", "lint"},
			wantStart: "test",
			wantStop:  "lint",
		},
		{
			name:            "equals values preserve pipeline arguments",
			args:            []string{"--name=gate", "--start-at=test", "--region", "west", "--stop-at=lint"},
			wantStart:       "test",
			wantStop:        "lint",
			wantPassthrough: []string{"--region", "west"},
		},
		{
			name:            "runtime spellings are pipeline arguments",
			args:            []string{"--name", "gate", "--sw-start-at", "test", "--sw-stop-at=lint"},
			wantPassthrough: []string{"--sw-start-at", "test", "--sw-stop-at=lint"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, help, err := parsePipelinePlanArgs(test.args)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if help {
				t.Fatal("range flags requested help")
			}
			if got.startAt != test.wantStart || got.stopAt != test.wantStop {
				t.Fatalf("range = %q..%q, want %q..%q", got.startAt, got.stopAt, test.wantStart, test.wantStop)
			}
			if !slices.Equal(got.passthrough, test.wantPassthrough) {
				t.Fatalf("passthrough = %v, want %v", got.passthrough, test.wantPassthrough)
			}
		})
	}
}

func TestParsePipelinePlanArgs_RangeFlagsRequireValues(t *testing.T) {
	for _, flag := range []string{"--start-at", "--stop-at"} {
		t.Run(flag, func(t *testing.T) {
			if _, _, err := parsePipelinePlanArgs([]string{"--name", "gate", flag}); err == nil {
				t.Fatalf("%s without a value succeeded", flag)
			}
		})
	}
}
