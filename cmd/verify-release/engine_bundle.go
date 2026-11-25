package main

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

// safety: these are the runner image platforms, and each one ships an engine Pods will verify.
var engineBundleArches = []string{"amd64", "arm64"}

// safety: a deploy bakes these bytes into an immutable image, so they pass the runner's own VerifyRelease first.
func verifyEngineBundle(release string, keys []ed25519.PublicKey) error {
	version := filepath.Base(release)
	read := func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(release, name)) }
	manifest, err := read("SHA256SUMS")
	if err != nil {
		return err
	}
	manifestSignature, err := read("SHA256SUMS.sig")
	if err != nil {
		return err
	}
	for _, arch := range engineBundleArches {
		target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: "linux", GOARCH: arch}
		name, err := target.NameForVersion(version)
		if err != nil {
			return err
		}
		bundle := engine.ReleaseBundle{Manifest: manifest, ManifestSignature: manifestSignature}
		if bundle.Asset, err = read(name); err != nil {
			return err
		}
		if bundle.AssetSignature, err = read(name + ".sig"); err != nil {
			return err
		}
		if _, err := engine.VerifyRelease(version, target, bundle, keys); err != nil {
			return fmt.Errorf("engine %s %s: %w", version, name, err)
		}
	}
	return nil
}
