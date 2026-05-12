package main

import (
	"strings"
	"testing"
)

// TestResolveOutputFormat covers the four-quadrant matrix: --json on
// its own, --output=json on its own, both set in agreement, and both
// set in disagreement. An earlier resolver errored on quadrant (1)
// when a leaf registered "pretty" as the pflag default ("--json and
// -o pretty disagree"), because the resolver couldn't distinguish
// "user typed -o pretty" from "pretty was the default and the user
// never touched -o." Tracking outputChanged explicitly fixes that
// asymmetry.
func TestResolveOutputFormat(t *testing.T) {
	tests := []struct {
		name string
		// Inputs to resolveOutputFormat:
		outFmt        string
		outputChanged bool
		jsonAlias     bool
		// Expected:
		want    string
		wantErr string // substring match; "" = expect no error
	}{
		// Quadrant 1: only --json set. Default outFmt may be "pretty"
		// (action.go callers) or "" (info.go callers); in both cases
		// the resolver should yield "json" because outputChanged=false.
		{
			name:          "only --json (default outFmt=pretty)",
			outFmt:        "pretty",
			outputChanged: false,
			jsonAlias:     true,
			want:          "json",
		},
		{
			name:          "only --json (default outFmt=empty)",
			outFmt:        "",
			outputChanged: false,
			jsonAlias:     true,
			want:          "json",
		},
		// "table" is a back-compat input alias for "pretty"; same path.
		{
			name:          "back-compat -o table normalizes to pretty",
			outFmt:        "table",
			outputChanged: true,
			jsonAlias:     false,
			want:          "pretty",
		},
		// Quadrant 2: only --output=json. No --json alias; resolver
		// returns the explicit value.
		{
			name:          "only --output=json",
			outFmt:        "json",
			outputChanged: true,
			jsonAlias:     false,
			want:          "json",
		},
		// Quadrant 3: both set, both agree. No error; resolver returns
		// "json" (the agreed value).
		{
			name:          "both --json and --output=json (agree)",
			outFmt:        "json",
			outputChanged: true,
			jsonAlias:     true,
			want:          "json",
		},
		// Quadrant 4: both set, disagree. Real conflict; resolver
		// surfaces the error so the user fixes their invocation.
		{
			name:          "both --json and --output=pretty (disagree)",
			outFmt:        "pretty",
			outputChanged: true,
			jsonAlias:     true,
			wantErr:       "--json and -o pretty disagree",
		},
		{
			name:          "both --json and --output=plain (disagree)",
			outFmt:        "plain",
			outputChanged: true,
			jsonAlias:     true,
			wantErr:       "--json and -o plain disagree",
		},
		// Defaults: nothing set on either side -> "pretty". Regression
		// check.
		{
			name:          "nothing set (default empty)",
			outFmt:        "",
			outputChanged: false,
			jsonAlias:     false,
			want:          "pretty",
		},
		{
			name:          "nothing set (default pretty)",
			outFmt:        "pretty",
			outputChanged: false,
			jsonAlias:     false,
			want:          "pretty",
		},
		// Explicit --output=plain (without --json) is honored.
		{
			name:          "only --output=plain",
			outFmt:        "plain",
			outputChanged: true,
			jsonAlias:     false,
			want:          "plain",
		},
		// Invalid --output value errors regardless of --json.
		{
			name:          "invalid --output value",
			outFmt:        "yaml",
			outputChanged: true,
			jsonAlias:     false,
			wantErr:       `must be one of pretty|json|plain, got "yaml"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveOutputFormat(tc.outFmt, tc.outputChanged, tc.jsonAlias, "test cmd")
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q; got %q with no error", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
