package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseInfoVersion_Classification(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		buildType string
		semver    string
		isRelease bool
		isDirty   bool
	}{
		{"published release", "v0.15.0", "release", "v0.15.0", true, false},
		{"dev build off latest tag", "v0.15.0-dev+6f2bf80", "local-clean", "v0.15.0", false, false},
		{"dirty dev build", "v0.15.0-dev+6f2bf80+dirty", "local-dirty", "v0.15.0", false, true},
		{"tag-derived pseudo is not a release", "v1.6.2-0.20260708072712-6f2bf808d2d0", "local-clean", "", false, false},
		{"go devel", "(devel)", "devel", "", false, false},
		{"missing metadata", "(unknown)", "unknown", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseInfoVersion(c.raw)
			if got.BuildType != c.buildType {
				t.Errorf("BuildType = %q, want %q", got.BuildType, c.buildType)
			}
			if got.Semver != c.semver {
				t.Errorf("Semver = %q, want %q", got.Semver, c.semver)
			}
			if got.IsRelease != c.isRelease {
				t.Errorf("IsRelease = %v, want %v", got.IsRelease, c.isRelease)
			}
			if got.IsDirty != c.isDirty {
				t.Errorf("IsDirty = %v, want %v", got.IsDirty, c.isDirty)
			}
		})
	}
}

func TestInfoLeadsWithMissingDeclaredHooks(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	info := Info{Project: InfoProject{
		Found: true, SparkwingDir: filepath.Join(f.repo, ".sparkwing"),
	}}

	steps := nextStepsFor(info, false)
	if len(steps) == 0 || !strings.Contains(steps[0].Command, "sparkwing pipeline hooks install") ||
		!strings.Contains(steps[0].Purpose, "pre-commit") {
		t.Fatalf("info next steps = %+v, want missing hook repair first", steps)
	}
}
