package jobs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScaffoldFallbackProblem(t *testing.T) {
	cases := []struct {
		name   string
		pinned string
		latest string
		wantOK bool
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

func TestReadScaffoldFallbackVersionReadsCheckoutSource(t *testing.T) {
	repoRoot := t.TempDir()
	dir := filepath.Join(repoRoot, "pkg", "scaffold")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "version.go"), []byte(`package scaffold

const FallbackSDKVersion = "v0.99.0"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readScaffoldFallbackVersion(repoRoot)
	if err != nil {
		t.Fatalf("readScaffoldFallbackVersion: %v", err)
	}
	if got != "v0.99.0" {
		t.Fatalf("readScaffoldFallbackVersion() = %q, want v0.99.0", got)
	}
}

func TestShouldCheckLocalReplaceFreshness(t *testing.T) {
	repoRoot := t.TempDir()
	cases := []struct {
		name      string
		relMod    string
		module    string
		local     string
		options   VersionFreshnessOptions
		wantCheck bool
	}{
		{
			name:      "regular push checks self replace",
			relMod:    ".sparkwing/go.mod",
			module:    sdkModulePath,
			local:     repoRoot,
			wantCheck: true,
		},
		{
			name:   "release pipeline allows same-checkout self replace",
			relMod: ".sparkwing/go.mod",
			module: sdkModulePath,
			local:  repoRoot,
			options: VersionFreshnessOptions{
				AllowReleaseLineSelfReplace: true,
			},
			wantCheck: false,
		},
		{
			name:   "release pipeline still checks other local replaces",
			relMod: "examples/go.mod",
			module: sdkModulePath,
			local:  repoRoot,
			options: VersionFreshnessOptions{
				AllowReleaseLineSelfReplace: true,
			},
			wantCheck: true,
		},
		{
			name:   "release pipeline still checks other modules",
			relMod: ".sparkwing/go.mod",
			module: "github.com/sparkwing-dev/sparks-core",
			local:  repoRoot,
			options: VersionFreshnessOptions{
				AllowReleaseLineSelfReplace: true,
			},
			wantCheck: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shouldCheckLocalReplaceFreshness(c.relMod, c.module, c.local, repoRoot, c.options)
			if got != c.wantCheck {
				t.Errorf("shouldCheckLocalReplaceFreshness() = %v, want %v", got, c.wantCheck)
			}
		})
	}
}
