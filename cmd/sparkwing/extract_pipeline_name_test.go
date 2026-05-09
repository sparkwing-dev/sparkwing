package main

import (
	"reflect"
	"strings"
	"testing"
)

// extractPipelineName enforces strict ordering for `-C` /
// `--change-directory` against the pipeline-name positional. The
// flag must appear BEFORE the positional, otherwise the parser
// would silently treat `-C` as the pipeline name (the regression:
// `wing run -C /path lint --on prod` dispatched
// "--change-directory" against the wrong repo).
func TestExtractPipelineName_StrictOrderC(t *testing.T) {
	cases := []struct {
		name      string
		in        []string
		wantName  string
		wantRest  []string
		wantErrIn string // substring; empty = no error
	}{
		{
			name:     "short -C before pipeline name",
			in:       []string{"-C", "/path", "foo"},
			wantName: "foo",
			wantRest: []string{"-C", "/path"},
		},
		{
			name:     "long --change-directory before pipeline name",
			in:       []string{"--change-directory", "/path", "foo"},
			wantName: "foo",
			wantRest: []string{"--change-directory", "/path"},
		},
		{
			name:     "--change-directory=value form before name",
			in:       []string{"--change-directory=/path", "foo"},
			wantName: "foo",
			wantRest: []string{"--change-directory=/path"},
		},
		{
			name:     "-C composes with other wing flag before name",
			in:       []string{"-C", "/path", "--on", "prod", "foo"},
			wantName: "foo",
			wantRest: []string{"-C", "/path", "--on", "prod"},
		},
		{
			name:     "-C with pipeline flags after the name",
			in:       []string{"-C", "/path", "foo", "--target", "prod"},
			wantName: "foo",
			wantRest: []string{"-C", "/path", "--target", "prod"},
		},
		{
			name:      "rejects -C after pipeline name",
			in:        []string{"foo", "-C", "/path"},
			wantErrIn: "ambiguous flag position: -C must precede the pipeline name \"foo\"",
		},
		{
			name:      "rejects --change-directory after pipeline name",
			in:        []string{"foo", "--change-directory", "/path"},
			wantErrIn: "ambiguous flag position: --change-directory must precede",
		},
		{
			name:      "rejects --change-directory=value after pipeline name",
			in:        []string{"foo", "--change-directory=/path"},
			wantErrIn: "ambiguous flag position: --change-directory=/path must precede",
		},
		{
			name:     "-- delimiter passes pipeline-flag-looking tokens through",
			in:       []string{"foo", "--", "--my-pipeline-flag", "value"},
			wantName: "foo",
			wantRest: []string{"--my-pipeline-flag", "value"},
		},
		{
			name:      "no pipeline name returns error",
			in:        []string{"-C", "/path"},
			wantErrIn: "pipeline name required",
		},
		{
			name:      "-- without pipeline name errors",
			in:        []string{"--", "foo"},
			wantErrIn: "pipeline name required before `--`",
		},
		{
			name:     "non-strict-order wing flag after name still allowed (preserves wing build --on prod muscle memory)",
			in:       []string{"foo", "--on", "prod"},
			wantName: "foo",
			wantRest: []string{"--on", "prod"},
		},
		{
			name:     "non-strict-order wing flag before name composes with -C",
			in:       []string{"--on", "prod", "-C", "/path", "foo"},
			wantName: "foo",
			wantRest: []string{"--on", "prod", "-C", "/path"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotRest, err := extractPipelineName(tc.in)
			if tc.wantErrIn != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (name=%q rest=%v)", tc.wantErrIn, gotName, gotRest)
				}
				if !strings.Contains(err.Error(), tc.wantErrIn) {
					t.Errorf("error = %v, want substring %q", err, tc.wantErrIn)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotName != tc.wantName {
				t.Errorf("name = %q, want %q", gotName, tc.wantName)
			}
			if !reflect.DeepEqual(gotRest, tc.wantRest) {
				t.Errorf("rest = %#v, want %#v", gotRest, tc.wantRest)
			}
		})
	}
}

// extractPipelineName + parseWingFlags must compose so that the wing
// flags surface unchanged regardless of token position (subject to
// the strict-order rule for -C). This pins the integration so a
// refactor of either side is caught.
func TestExtractPipelineName_ComposesWithParseWingFlags(t *testing.T) {
	args := []string{"-C", "/path", "--on", "prod", "deploy", "--target", "v1"}
	name, rest, err := extractPipelineName(args)
	if err != nil {
		t.Fatalf("extractPipelineName: %v", err)
	}
	if name != "deploy" {
		t.Errorf("name = %q, want %q", name, "deploy")
	}
	wf, pass := parseWingFlags(rest)
	if wf.changeDir != "/path" {
		t.Errorf("changeDir = %q, want %q", wf.changeDir, "/path")
	}
	if wf.on != "prod" {
		t.Errorf("on = %q, want %q", wf.on, "prod")
	}
	wantPass := []string{"--target", "v1"}
	if !reflect.DeepEqual(pass, wantPass) {
		t.Errorf("pass = %#v, want %#v", pass, wantPass)
	}
}
