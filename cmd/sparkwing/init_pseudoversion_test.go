package main

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/scaffold"
)

// TestFallbackSDKVersionIsResolvable guards the scaffold fallback pin
// against being set to a value the scaffolder can't require -- a
// pseudo-version, a "+dirty" marker, or a "(devel)" placeholder. The
// freshness gate keeps it from lagging the latest release; this keeps it
// a real release version at all. Offline: no proxy resolution.
func TestFallbackSDKVersionIsResolvable(t *testing.T) {
	if !isResolvableModuleVersion(scaffold.FallbackSDKVersion) {
		t.Errorf("scaffold.FallbackSDKVersion = %q is not a resolvable release version", scaffold.FallbackSDKVersion)
	}
}

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
		{"0.8.0", false},
		{"v0.8.0+dirty", false},
		{"v1.0.0-20260531005950-041d1c11f150", false},
		{"v0.8.1-0.20260606014656-114f6846819b", false},
		{"v0.6.3-pre.0.20260531005950-041d1c11f150", false},
		{"v0.8.1-0.20260606014656-114f6846819b+dirty", false},
	}
	for _, c := range cases {
		if got := isResolvableModuleVersion(c.v); got != c.want {
			t.Errorf("isResolvableModuleVersion(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}
