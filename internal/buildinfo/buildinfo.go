// Package buildinfo reports an executable's embedded build identity.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"golang.org/x/mod/semver"
)

// Identity is the self-reported identity of a Sparkwing executable.
type Identity struct {
	Binary       string `json:"binary"`
	Version      string `json:"version"`
	Commit       string `json:"commit,omitempty"`
	RevisionTime string `json:"revision_time,omitempty"`
	Modified     bool   `json:"modified"`
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
}

// Read returns the build identity for binary. A linker-stamped version wins
// over module metadata so release builds retain their immutable tag.
func Read(binary, stampedVersion string) Identity {
	identity := Identity{Binary: binary, Version: stampedVersion, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if info, ok := debug.ReadBuildInfo(); ok {
		if identity.Version == "" {
			identity.Version = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				identity.Commit = setting.Value
			case "vcs.time":
				identity.RevisionTime = setting.Value
			case "vcs.modified":
				identity.Modified = setting.Value == "true"
			}
		}
	}
	if identity.Version == "" {
		identity.Version = "(unknown)"
	}
	return identity
}

// Expectation binds the product, release and platform an executable must
// report before it can be installed under that identity.
type Expectation struct {
	Binary  string
	Version string
	GOOS    string
	GOARCH  string
}

// Verify rejects an identity that does not exactly match expected.
func Verify(identity Identity, expected Expectation) error {
	if identity.Binary != expected.Binary {
		return fmt.Errorf("binary reports product %q, want %q", identity.Binary, expected.Binary)
	}
	if identity.Version != expected.Version {
		return fmt.Errorf("%s reports version %q, want %q", expected.Binary, identity.Version, expected.Version)
	}
	if identity.GOOS != expected.GOOS || identity.GOARCH != expected.GOARCH {
		return fmt.Errorf("%s reports platform %s/%s, want %s/%s",
			expected.Binary, identity.GOOS, identity.GOARCH, expected.GOOS, expected.GOARCH)
	}
	return nil
}

// IsReleaseVersion reports whether version is a canonical stable release tag.
func IsReleaseVersion(version string) bool {
	return semver.IsValid(version) && semver.Canonical(version) == version &&
		semver.Prerelease(version) == "" && semver.Build(version) == ""
}
