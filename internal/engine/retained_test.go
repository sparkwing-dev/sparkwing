package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestReplacementEnsureKeepsAcceptedLocalBuild(t *testing.T) {
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	version := "v0.69.2-dev+retained"
	body := append(fixtureExecutable(t, version, target), []byte("retained executable trailer")...)
	identity := engineid.Identity{
		Pin:     engineid.Pin{SDK: engineid.GoSDK, Version: "v0.0.0", Replacement: "../sdk"},
		Version: version, ManifestDigest: digest(body),
	}
	dir := t.TempDir()
	name := identity.ManifestDigest
	if target.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o700); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(Config{CacheDirectory: t.TempDir(), LocalBuildDirectory: dir})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := resolver.Ensure(t.Context(), identity, target)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Identity != identity || selected.AssetDigest != identity.ManifestDigest || selected.Executable != path {
		t.Fatalf("retained engine selection = %+v", selected)
	}
	if err := selected.verifyInstalledEngine(t.Context()); err != nil {
		t.Fatalf("retained SDK installer verification: %v", err)
	}
	installed, err := resolver.Resolve(t.Context(), identity.Pin, target)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Executable == path {
		t.Fatal("new replacement resolution selected an earlier accepted build")
	}
	strict, err := NewResolver(Config{CacheDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strict.Ensure(t.Context(), identity, target); err == nil || !strings.Contains(err.Error(), "planned on engine "+identity.Version) {
		t.Fatalf("default resolver accepted retained build: %v", err)
	}
	for _, mutation := range []string{"path", "pin", "digest", "version", "target"} {
		t.Run("changed-"+mutation, func(t *testing.T) {
			changed := selected
			switch mutation {
			case "path":
				changed.Executable = installed.Executable
			case "pin":
				changed.Identity.Pin.Replacement = "../other-sdk"
			case "digest":
				changed.AssetDigest = strings.Repeat("0", 64)
			case "version":
				changed.Identity.Version = installed.Identity.Version
			case "target":
				changed.Target.GOARCH = "other"
			}
			if err := changed.verifyInstalledEngine(t.Context()); err == nil {
				t.Fatal("mutated retained engine reached SDK installation")
			}
		})
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := append([]byte(nil), body...)
	changed[len(changed)-1] ^= 1
	if err := os.WriteFile(path, changed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := selected.verifyInstalledEngine(t.Context()); err == nil {
		t.Fatal("retained SDK engine mutation escaped full-byte verification")
	}
}

func TestReplacementEnsureRejectsUnusableRetainedBuild(t *testing.T) {
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	version := "v0.69.2-dev+retained"
	body := fixtureExecutable(t, version, target)
	identity := engineid.Identity{
		Pin:     engineid.Pin{SDK: engineid.GoSDK, Version: "v0.0.0", Replacement: "../sdk"},
		Version: version, ManifestDigest: digest(body),
	}
	for _, failure := range []string{"missing", "corrupt", "digest", "version", "unsafe-digest", "not-executable"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			accepted, contents, mode := identity, body, os.FileMode(0o700)
			switch failure {
			case "corrupt":
				contents = []byte("corrupt executable")
			case "digest":
				contents = append(append([]byte(nil), body...), 0)
			case "version":
				current, err := installedEngineVersion()
				if err != nil {
					t.Fatal(err)
				}
				accepted.Version = current
			case "unsafe-digest":
				accepted.ManifestDigest = "../../outside"
			case "not-executable":
				if runtime.GOOS == "windows" {
					t.Skip("Windows executable permissions")
				}
				mode = 0o600
			}
			if failure != "missing" && failure != "unsafe-digest" {
				name := accepted.ManifestDigest
				if target.GOOS == "windows" {
					name += ".exe"
				}
				if err := os.WriteFile(filepath.Join(dir, name), contents, mode); err != nil {
					t.Fatal(err)
				}
			}
			resolver, err := NewResolver(Config{CacheDirectory: t.TempDir(), LocalBuildDirectory: dir})
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.Ensure(t.Context(), accepted, target)
			requireCode(t, err, apierr.IntegrityFailure)
		})
	}
}

func TestReplacementEnsureRejectsForeignRetainedPlatform(t *testing.T) {
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	foreign := target
	foreign.GOARCH = "arm64"
	if foreign.GOARCH == runtime.GOARCH {
		foreign.GOARCH = "amd64"
	}
	version := "v0.69.2-dev+retained"
	body := fixtureExecutable(t, version, foreign)
	identity := engineid.Identity{
		Pin:     engineid.Pin{SDK: engineid.GoSDK, Version: "v0.0.0", Replacement: "../sdk"},
		Version: version, ManifestDigest: digest(body),
	}
	dir := t.TempDir()
	name := identity.ManifestDigest
	if target.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o700); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(Config{CacheDirectory: t.TempDir(), LocalBuildDirectory: dir})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Ensure(t.Context(), identity, target)
	requireCode(t, err, apierr.IntegrityFailure)
}
