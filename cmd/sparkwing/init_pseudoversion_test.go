package main

import "testing"

func TestIsResolvableModuleVersion(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"v0.8.0", true},
		{"v1.2.3", true},
		{"", false},
		{"(devel)", false},
		{"(unknown)", false},
		{"0.8.0", false}, // missing v prefix
		{"v0.8.0+dirty", false},
		// Pseudo-versions in all three forms must be rejected (their
		// commit isn't resolvable from a fresh `go mod` install).
		{"v1.0.0-20260531005950-041d1c11f150", false},       // no base tag
		{"v0.8.1-0.20260606014656-114f6846819b", false},     // commit after release vX.Y.Z
		{"v0.6.3-pre.0.20260531005950-041d1c11f150", false}, // pre-release base
		{"v0.8.1-0.20260606014656-114f6846819b+dirty", false},
	}
	for _, c := range cases {
		if got := isResolvableModuleVersion(c.v); got != c.want {
			t.Errorf("isResolvableModuleVersion(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}
