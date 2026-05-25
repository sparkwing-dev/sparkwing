package main

import (
	"slices"
	"strings"
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

func TestParseRunFlags_Profile(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--profile", "prod"}, "prod"},
		{"equals-form", []string{"--profile=prod"}, "prod"},
		{"empty-trailing-flag-falls-through", []string{"--profile"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, pass := parseRunFlags(tc.args)
			if wf.profile != tc.want {
				t.Errorf("profile = %q, want %q", wf.profile, tc.want)
			}
			if tc.want == "" && !slices.Contains(pass, "--profile") {
				t.Errorf("incomplete --profile should pass through; got passthrough=%v", pass)
			}
		})
	}
}

// --profile picks the storage profile; --target picks the pipeline's
// deployment target. They are distinct, both consumed by parseRunFlags.
func TestParseRunFlags_ProfileAndTarget(t *testing.T) {
	wf, _ := parseRunFlags([]string{"--profile", "local", "--target", "prod"})
	if wf.profile != "local" || wf.target != "prod" {
		t.Errorf("got profile=%q target=%q, want local/prod", wf.profile, wf.target)
	}
}

// The retired --sw-profile flag is no longer parsed; it falls through to
// passthrough where checkRetiredWhereFlags catches it with a pointer.
func TestParseRunFlags_RetiredSwProfileFallsThrough(t *testing.T) {
	wf, pass := parseRunFlags([]string{"--sw-profile", "remote"})
	if wf.profile != "" {
		t.Errorf("--sw-profile should not set profile; got %q", wf.profile)
	}
	if err := checkRetiredWhereFlags(pass); err == nil || !strings.Contains(err.Error(), "--sw-profile") {
		t.Errorf("checkRetiredWhereFlags: want --sw-profile pointer, got %v", err)
	}
}
