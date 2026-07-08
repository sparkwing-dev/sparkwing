package main

import "testing"

func TestClassifyDowngrade(t *testing.T) {
	cases := []struct {
		name     string
		current  string
		resolved string
		want     downgradeKind
	}{
		{
			name:     "unpublished v1 pseudo-version re-baselines to published latest",
			current:  "v1.6.2-0.20260612080412-e90de2aa40b0",
			resolved: "v0.9.1",
			want:     downgradeRebaseline,
		},
		{
			name:     "v0 pseudo-version above latest re-baselines without force",
			current:  "v0.14.0-0.20260612080412-e90de2aa40b0",
			resolved: "v0.13.0",
			want:     downgradeRebaseline,
		},
		{
			name:     "dirty local build re-baselines without force",
			current:  "v0.14.0+dirty",
			resolved: "v0.13.0",
			want:     downgradeRebaseline,
		},
		{
			name:     "published release to older published release needs force",
			current:  "v0.13.0",
			resolved: "v0.9.1",
			want:     downgradeNeedsForce,
		},
		{
			name:     "upgrade to newer release is allowed",
			current:  "v0.9.1",
			resolved: "v0.13.0",
			want:     downgradeAllowed,
		},
		{
			name:     "same version is allowed",
			current:  "v0.13.0",
			resolved: "v0.13.0",
			want:     downgradeAllowed,
		},
		{
			name:     "real v1 launch over v0 install is a normal upgrade",
			current:  "v0.13.0",
			resolved: "v1.0.0",
			want:     downgradeAllowed,
		},
		{
			name:     "non-semver current is allowed",
			current:  "(devel)",
			resolved: "v0.13.0",
			want:     downgradeAllowed,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyDowngrade(c.current, c.resolved); got != c.want {
				t.Errorf("classifyDowngrade(%q, %q) = %v, want %v", c.current, c.resolved, got, c.want)
			}
		})
	}
}
