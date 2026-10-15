package main

import (
	"strings"
	"testing"
)

func TestRunIDFromArgs(t *testing.T) {
	cases := []struct {
		name    string
		rest    []string
		flag    string
		want    string
		wantErr string
	}{
		{name: "flag only", rest: nil, flag: "run-a", want: "run-a"},
		{name: "positional only", rest: []string{"run-a"}, want: "run-a"},
		{name: "neither", rest: nil, wantErr: "a run id is required"},
		{name: "both", rest: []string{"run-a"}, flag: "run-b", wantErr: "given twice"},
		{name: "two positionals", rest: []string{"run-a", "run-b"}, wantErr: "unexpected positional"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runIDFromArgs("sparkwing runs status", tc.rest, tc.flag)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("id = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunsStatusRequiresKindNamedSelector(t *testing.T) {
	err := runRunsStatus(t.Context(), []string{"r1", "--profile", "prod"})
	if exitCodeFor(err) != 2 {
		t.Fatalf("bare run ID accepted: %v", err)
	}
}
