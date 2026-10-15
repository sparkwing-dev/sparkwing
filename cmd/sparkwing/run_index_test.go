package main

import "testing"

func TestParseRunFlags_Index(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--sw-index", "/tmp/snap.index"}, "/tmp/snap.index"},
		{"equals-form", []string{"--sw-index=/tmp/snap.index"}, "/tmp/snap.index"},
		{"missing-value-is-a-usage-error", []string{"--sw-index"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, pass := parseRunFlags(tc.args)
			if wf.index != tc.want {
				t.Errorf("index = %q, want %q", wf.index, tc.want)
			}
			if tc.want == "" && (wf.parseErr == nil || len(pass) != 0) {
				t.Errorf("incomplete --sw-index must be a usage error; got passthrough=%v", pass)
			}
		})
	}
}
