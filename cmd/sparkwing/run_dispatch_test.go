package main

import (
	"slices"
	"testing"
)

func TestParseRunFlags_Only(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--sw-only", "test-phase-*"}, "test-phase-*"},
		{"equals-form", []string{"--sw-only=test-phase-*"}, "test-phase-*"},
		{"empty-trailing-flag-falls-through", []string{"--sw-only"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, pass := parseRunFlags(tc.args)
			if wf.only != tc.want {
				t.Errorf("only = %q, want %q", wf.only, tc.want)
			}
			if tc.want == "" && !slices.Contains(pass, "--sw-only") {
				t.Errorf("incomplete --sw-only should pass through; got passthrough=%v", pass)
			}
		})
	}
}

func TestParseRunFlags_NoCache(t *testing.T) {
	wf, pass := parseRunFlags([]string{"--sw-no-cache"})
	if !wf.noCache {
		t.Errorf("noCache: want true got false")
	}
	if len(pass) != 0 {
		t.Errorf("passthrough should be empty, got %v", pass)
	}
}

func TestParseRunFlags_LocalOnly(t *testing.T) {
	wf, pass := parseRunFlags([]string{"--sw-local-only"})
	if !wf.localOnly {
		t.Errorf("localOnly: want true got false")
	}
	if len(pass) != 0 {
		t.Errorf("passthrough should be empty, got %v", pass)
	}
}

func TestParseRunFlags_OnlyAndNoCacheCoexist(t *testing.T) {
	wf, _ := parseRunFlags([]string{"--sw-only=lint-*", "--sw-no-cache"})
	if wf.only != "lint-*" {
		t.Errorf("only = %q, want lint-*", wf.only)
	}
	if !wf.noCache {
		t.Errorf("noCache: want true got false")
	}
}

func TestParseRunFlags_UnknownFlagsPassThrough(t *testing.T) {
	_, pass := parseRunFlags([]string{"--sw-only=*", "--user-flag", "v", "--other"})
	wantPass := []string{"--user-flag", "v", "--other"}
	if !slices.Equal(pass, wantPass) {
		t.Errorf("passthrough = %v, want %v", pass, wantPass)
	}
}
