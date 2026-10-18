package releaseasset

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// currentMatrixSince is the first release published without sparkwing-cache, including its pre-releases.
const currentMatrixSince = "v0.68.0-0"

// NamesForVersion preserves the published asset matrix independently of today's publishers.
func NamesForVersion(version string) ([]string, error) {
	if !semver.IsValid(version) || semver.Canonical(version) != version || semver.Build(version) != "" || module.IsPseudoVersion(version) {
		return nil, fmt.Errorf("invalid release version %q", version)
	}
	if semver.Compare(version, currentMatrixSince) >= 0 {
		return Names(), nil
	}
	var names []string
	for _, binary := range []Binary{Sparkwing, "sparkwing-cache", SparkwingController, SparkwingRunner} {
		for _, goos := range []string{"darwin", "linux", "windows"} {
			if goos == "windows" && binary != Sparkwing && binary != SparkwingRunner {
				continue
			}
			for _, arch := range []string{"amd64", "arm64"} {
				name := string(binary) + "-" + goos + "-" + arch
				if goos == "windows" {
					name += ".exe"
				}
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// NameForVersion checks the retained release matrix rather than current publication targets.
func (target Target) NameForVersion(version string) (string, error) {
	names, err := NamesForVersion(version)
	if err != nil {
		return "", err
	}
	name := string(target.Binary) + "-" + target.GOOS + "-" + target.GOARCH
	if target.GOOS == "windows" {
		name += ".exe"
	}
	i := sort.SearchStrings(names, name)
	if i == len(names) || names[i] != name {
		return "", fmt.Errorf("release %s has no asset for %s", version, name)
	}
	return name, nil
}

// VerifyForVersion authenticates the exact closed release set and selected executable.
func VerifyForVersion(keys []ed25519.PublicKey, version string, manifest, manifestSignature []byte, target Target, asset, assetSignature []byte) (Verified, error) {
	names, err := NamesForVersion(version)
	if err != nil {
		return Verified{}, err
	}
	name, err := target.NameForVersion(version)
	if err != nil {
		return Verified{}, err
	}
	expected := make(map[string]bool, len(names))
	for _, name := range names {
		expected[name] = true
	}
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !expected[fields[1]] {
			return Verified{}, fmt.Errorf("release manifest has duplicate, unknown or malformed entries")
		}
		digest, err := hex.DecodeString(fields[0])
		if err != nil || len(digest) != sha256.Size {
			return Verified{}, fmt.Errorf("release manifest has malformed digest")
		}
		delete(expected, fields[1])
	}
	if len(expected) != 0 {
		return Verified{}, fmt.Errorf("release manifest is missing %d executables", len(expected))
	}
	for _, key := range keys {
		verified, err := verifyWithKey(key, manifest, manifestSignature, name, asset, assetSignature)
		if err == nil {
			return verified, nil
		}
	}
	return Verified{}, fmt.Errorf("release signatures or digest do not match the trusted manifest")
}
