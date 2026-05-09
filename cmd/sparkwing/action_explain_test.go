package main

import (
	"reflect"
	"testing"
)

// `sparkwing pipeline explain --name X -- <flags>` must forward
// <flags> to the inner pipeline binary so a sliced Plan (e.g.
// --skip artifact, --only build) renders the same DAG as
// `wing X --explain --skip artifact`.
//
// The literal "--" separator must be CONSUMED, not forwarded:
// Go's flag package stops flag processing at "--", so passing it
// through would cause every trailing token to be parsed as a
// positional and the slicing flags would silently no-op.
func TestParsePipelineExplainArgs_Passthrough(t *testing.T) {
	cases := []struct {
		name       string
		in         []string
		wantName   string
		wantOutput string
		wantJSON   bool
		wantAll    bool
		wantPass   []string
		wantHelp   bool
		wantErr    bool
	}{
		{
			name:     "trailing flags after -- forwarded without the separator",
			in:       []string{"--name", "release-platform", "--", "--skip", "artifact"},
			wantName: "release-platform",
			wantPass: []string{"--skip", "artifact"},
		},
		{
			name:     "-- with no trailing tokens leaves passthrough empty",
			in:       []string{"--name", "release-platform", "--"},
			wantName: "release-platform",
			wantPass: nil,
		},
		{
			name:     "unknown flags before -- still pass through (legacy shape)",
			in:       []string{"--name", "X", "--region", "us-west"},
			wantName: "X",
			wantPass: []string{"--region", "us-west"},
		},
		{
			name:     "-- preserves -- prefix flags after it",
			in:       []string{"--name", "X", "--", "--only", "build", "--region=us-west"},
			wantName: "X",
			wantPass: []string{"--only", "build", "--region=us-west"},
		},
		{
			name:     "wrapper flags after -- are NOT re-interpreted (raw passthrough)",
			in:       []string{"--name", "X", "--", "--json", "--all"},
			wantName: "X",
			wantPass: []string{"--json", "--all"},
			wantJSON: false,
			wantAll:  false,
		},
		{
			name:     "--json before -- still toggles JSON output",
			in:       []string{"--json", "--name", "X"},
			wantName: "X",
			wantJSON: true,
		},
		{
			name:       "--output=json equivalent",
			in:         []string{"--output=json", "--name", "X"},
			wantName:   "X",
			wantOutput: "json",
		},
		{
			name:     "--help short-circuits",
			in:       []string{"--name", "X", "--help"},
			wantHelp: true,
		},
		{
			name:    "--name without value errors",
			in:      []string{"--name"},
			wantErr: true,
		},
		{
			name:    "-o without value errors",
			in:      []string{"-o"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, helpRequested, err := parsePipelineExplainArgs(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (parsed=%+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if helpRequested != tc.wantHelp {
				t.Errorf("helpRequested = %v, want %v", helpRequested, tc.wantHelp)
			}
			if helpRequested {
				return
			}
			if got.pipeline != tc.wantName {
				t.Errorf("pipeline = %q, want %q", got.pipeline, tc.wantName)
			}
			if got.output != tc.wantOutput {
				t.Errorf("output = %q, want %q", got.output, tc.wantOutput)
			}
			if got.asJSON != tc.wantJSON {
				t.Errorf("asJSON = %v, want %v", got.asJSON, tc.wantJSON)
			}
			if got.all != tc.wantAll {
				t.Errorf("all = %v, want %v", got.all, tc.wantAll)
			}
			if !reflect.DeepEqual(got.passthrough, tc.wantPass) {
				t.Errorf("passthrough = %#v, want %#v", got.passthrough, tc.wantPass)
			}
		})
	}
}
