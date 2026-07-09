package jobs

import "testing"

func TestScaffoldFallbackProblem(t *testing.T) {
	cases := []struct {
		name   string
		pinned string
		latest string
		wantOK bool // true == no problem reported
	}{
		{"current", "v0.15.3", "v0.15.3", true},
		{"ahead", "v0.15.4", "v0.15.3", true},
		{"behind", "v0.8.1", "v0.15.3", false},
		{"behind by patch", "v0.15.2", "v0.15.3", false},
		{"invalid pin", "", "v0.15.3", false},
		{"non-semver pin", "(devel)", "v0.15.3", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scaffoldFallbackProblem(c.pinned, c.latest)
			if c.wantOK && got != "" {
				t.Errorf("scaffoldFallbackProblem(%q, %q) = %q, want no problem", c.pinned, c.latest, got)
			}
			if !c.wantOK && got == "" {
				t.Errorf("scaffoldFallbackProblem(%q, %q) reported no problem, want one", c.pinned, c.latest)
			}
		})
	}
}
