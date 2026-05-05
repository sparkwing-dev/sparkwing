package main

import (
	"reflect"
	"testing"
)

// parseWingFlags must strip wing-owned flags from the arg stream
// and leave everything else for the pipeline binary. The -C flag
// (and its long form --change-directory, both `--flag value` and
// `--flag=value` shapes) is the most recently added; cover both.
func TestParseWingFlags_ChangeDirectory(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		wantDir string
		wantPas []string
	}{
		{
			name:    "short -C with separate value",
			in:      []string{"-C", "../app2", "deploy"},
			wantDir: "../app2",
			wantPas: []string{"deploy"},
		},
		{
			name:    "long --change-directory with separate value",
			in:      []string{"--change-directory", "/abs/path", "deploy", "--prod"},
			wantDir: "/abs/path",
			wantPas: []string{"deploy", "--prod"},
		},
		{
			name:    "long --change-directory=value form",
			in:      []string{"--change-directory=../app2", "deploy"},
			wantDir: "../app2",
			wantPas: []string{"deploy"},
		},
		{
			name:    "no -C present leaves changeDir empty",
			in:      []string{"deploy", "--target", "prod"},
			wantDir: "",
			wantPas: []string{"deploy", "--target", "prod"},
		},
		{
			name:    "trailing -C with no value passes through (pipeline binary will complain)",
			in:      []string{"-C"},
			wantDir: "",
			wantPas: []string{"-C"},
		},
		{
			name:    "-C composes with --on",
			in:      []string{"-C", "../app2", "--on", "prod", "deploy"},
			wantDir: "../app2",
			wantPas: []string{"deploy"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, pass := parseWingFlags(tc.in)
			if wf.changeDir != tc.wantDir {
				t.Errorf("changeDir = %q, want %q", wf.changeDir, tc.wantDir)
			}
			if !reflect.DeepEqual(pass, tc.wantPas) {
				t.Errorf("passthrough = %#v, want %#v", pass, tc.wantPas)
			}
		})
	}
}
