package jobs

import (
	"os"
	"path/filepath"
	"strings"
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

func TestBumpFallbackSDKVersionFile(t *testing.T) {
	cases := []struct {
		name       string
		src        string
		version    string
		wantErr    bool
		wantResult string
	}{
		{
			name:       "bumps current version to newer",
			src:        `const FallbackSDKVersion = "v0.18.0"` + "\n",
			version:    "v0.19.0",
			wantResult: `const FallbackSDKVersion = "v0.19.0"` + "\n",
		},
		{
			name:       "bumps to same version (idempotent)",
			src:        `const FallbackSDKVersion = "v0.19.0"` + "\n",
			version:    "v0.19.0",
			wantResult: `const FallbackSDKVersion = "v0.19.0"` + "\n",
		},
		{
			name:    "missing pattern returns error",
			src:     "package scaffold\n",
			version: "v0.19.0",
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			scaffoldDir := filepath.Join(dir, "pkg", "scaffold")
			if err := os.MkdirAll(scaffoldDir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(scaffoldDir, "version.go")
			if err := os.WriteFile(path, []byte(c.src), 0o644); err != nil {
				t.Fatal(err)
			}
			err := bumpFallbackSDKVersionFile(dir, c.version)
			if c.wantErr {
				if err == nil {
					t.Error("bumpFallbackSDKVersionFile() returned nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("bumpFallbackSDKVersionFile() error: %v", err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(got), c.wantResult) {
				t.Errorf("version.go content = %q, want %q", string(got), c.wantResult)
			}
		})
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
