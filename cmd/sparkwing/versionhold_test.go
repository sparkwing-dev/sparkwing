package main

import (
	"strings"
	"testing"
)

func TestExceedsHold(t *testing.T) {
	cases := []struct {
		name   string
		target string
		hold   string
		want   bool
	}{
		{"next minor beyond a minor-series hold", "v0.16.0", "v0.15", true},
		{"patch within a minor-series hold is allowed", "v0.15.9", "v0.15", false},
		{"same minor, zero patch, allowed", "v0.15.0", "v0.15", false},
		{"major bump beyond a minor-series hold", "v1.0.0", "v0.15", true},
		{"patch beyond an exact ceiling", "v0.15.5", "v0.15.4", true},
		{"patch at an exact ceiling is allowed", "v0.15.4", "v0.15.4", false},
		{"patch below an exact ceiling is allowed", "v0.15.3", "v0.15.4", false},
		{"empty hold never blocks", "v9.9.9", "", false},
		{"malformed hold never blocks", "v0.16.0", "not-a-version", false},
		{"malformed target never blocks", "latest", "v0.15", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exceedsHold(c.target, c.hold); got != c.want {
				t.Errorf("exceedsHold(%q, %q) = %v, want %v", c.target, c.hold, got, c.want)
			}
		})
	}
}

func TestNormalizeHold(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"v0.15", "v0.15", false},
		{"0.15", "v0.15", false},
		{"v0.15.4", "v0.15.4", false},
		{"  v0.15  ", "v0.15", false},
		{"", "", true},
		{"garbage", "", true},
		{"v0.15.x", "", true},
	}
	for _, c := range cases {
		got, err := normalizeHold(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeHold(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("normalizeHold(%q) = (%q, %v), want (%q, nil)", c.in, got, err, c.want)
		}
	}
}

func TestHoldHasPatch(t *testing.T) {
	cases := map[string]bool{
		"v0.15":       false,
		"v0.15.4":     true,
		"v0.15.4-rc1": true,
		"v1":          false,
	}
	for in, want := range cases {
		if got := holdHasPatch(in); got != want {
			t.Errorf("holdHasPatch(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestHoldRefusalOmitsOverride pins the footgun rule: the refusal an
// agent sees names the operator setting but never advertises the
// escape hatch.
func TestHoldRefusalOmitsOverride(t *testing.T) {
	err := holdRefusal("v0.16.0", versionHold{Value: "v0.15", Source: "/tmp/version-hold"})
	msg := err.Error()
	if strings.Contains(strings.ToLower(msg), "override") {
		t.Fatalf("refusal message advertises the override flag:\n%s", msg)
	}
	if !strings.Contains(msg, "v0.15") || !strings.Contains(msg, "v0.16.0") {
		t.Fatalf("refusal message should name the hold and the target:\n%s", msg)
	}
}

func TestResolveVersionHold_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(versionHoldEnv, "")
	if err := runVersionHold([]string{"--set", "v0.15"}); err != nil {
		t.Fatalf("set hold: %v", err)
	}
	if h := resolveVersionHold(); h.Value != "v0.15" || h.Source == versionHoldEnv {
		t.Fatalf("file hold = %+v, want value v0.15 from file", h)
	}
	t.Setenv(versionHoldEnv, "v0.10")
	if h := resolveVersionHold(); h.Value != "v0.10" || h.Source != versionHoldEnv {
		t.Fatalf("env hold = %+v, want value v0.10 from env", h)
	}
}

func TestRunVersionHold_SetClearRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(versionHoldEnv, "")
	if err := runVersionHold([]string{"--set", "v0.15"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if h := resolveVersionHold(); h.Value != "v0.15" {
		t.Fatalf("after set: %+v", h)
	}
	if err := runVersionHold([]string{"--clear"}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if h := resolveVersionHold(); h.Value != "" {
		t.Fatalf("after clear: %+v, want empty", h)
	}
}
