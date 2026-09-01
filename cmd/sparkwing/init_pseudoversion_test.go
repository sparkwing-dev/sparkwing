package main

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/scaffold"
)

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

		{"v0.22.2-dev+1f8a9b98", false},
		{"v0.22.2-dev", false},
		{"v1.0.0+20130313144700", false},
		{"v2.0.0+incompatible", true},
	}
	for _, c := range cases {
		if got := isResolvableModuleVersion(c.v); got != c.want {
			t.Errorf("isResolvableModuleVersion(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}
