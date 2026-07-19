package jobs

import (
	"context"
	"os"
	"os/exec"
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

func TestCommitSparkwingPinBump(t *testing.T) {
	mustGit := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	initRepo := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		mustGit(t, dir, "init")
		mustGit(t, dir, "config", "user.email", "test@example.com")
		mustGit(t, dir, "config", "user.name", "Test")
		return dir
	}

	createBumpFiles := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "pkg", "scaffold"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, ".sparkwing"), 0o755); err != nil {
			t.Fatal(err)
		}
		for path, content := range map[string]string{
			filepath.Join("pkg", "scaffold", "version.go"): "v0.19.0",
			filepath.Join(".sparkwing", "go.mod"):           "module test",
			filepath.Join(".sparkwing", "go.sum"):           "",
		} {
			if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("creates commit with correct message and staged files", func(t *testing.T) {
		dir := initRepo(t)
		createBumpFiles(t, dir)

		if err := commitSparkwingPinBump(context.Background(), dir, "v0.19.0"); err != nil {
			t.Fatalf("commitSparkwingPinBump() error: %v", err)
		}

		subject, err := captureGit(context.Background(), dir, "log", "--format=%s", "-1")
		if err != nil {
			t.Fatalf("git log: %v", err)
		}
		if got := strings.TrimSpace(subject); got != "chore: bump sparkwing pin to v0.19.0" {
			t.Errorf("commit subject = %q, want %q", got, "chore: bump sparkwing pin to v0.19.0")
		}

		filesOut, err := captureGit(context.Background(), dir, "show", "--name-only", "--format=", "HEAD")
		if err != nil {
			t.Fatalf("git show: %v", err)
		}
		for _, f := range []string{"pkg/scaffold/version.go", ".sparkwing/go.mod", ".sparkwing/go.sum"} {
			if !strings.Contains(filesOut, f) {
				t.Errorf("committed files: %q not found in output %q", f, filesOut)
			}
		}
	})

	t.Run("returns error when staged paths do not exist", func(t *testing.T) {
		dir := initRepo(t)

		if err := commitSparkwingPinBump(context.Background(), dir, "v0.19.0"); err == nil {
			t.Error("commitSparkwingPinBump() returned nil, want error")
		}
	})

	t.Run("returns error when nothing is staged for commit", func(t *testing.T) {
		dir := initRepo(t)
		createBumpFiles(t, dir)

		mustGit(t, dir, "add", "--", "pkg/scaffold/version.go", ".sparkwing/go.mod", ".sparkwing/go.sum")
		mustGit(t, dir, "commit", "-m", "initial")

		if err := commitSparkwingPinBump(context.Background(), dir, "v0.19.0"); err == nil {
			t.Error("commitSparkwingPinBump() returned nil, want error when nothing to commit")
		}
	})
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
