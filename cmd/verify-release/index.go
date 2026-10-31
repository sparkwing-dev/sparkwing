package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
)

func publishReleaseIndex(previous, out, required string, bootstrap bool) error {
	if previous == "" || !stableIndexVersion(required) {
		return errors.New("--previous-index and --require-version v0.X.Y are required")
	}
	key, err := releaseauth.PrivateKey(os.Getenv("SPARKWING_RELEASE_SIGNING_KEY"))
	if err != nil {
		return err
	}
	if base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)) != releaseauth.ProductionPublicKey {
		return errors.New("index signing key is not the production update key")
	}
	keys, err := releaseauth.PublicKeys()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	index, err := readReleaseIndex(previous, bootstrap, keys, now)
	if err != nil {
		return err
	}
	pages, err := publishedReleasePages()
	if err != nil {
		return err
	}
	index, err = nextReleaseIndex(index, pages, required, now)
	if err != nil {
		return err
	}
	return writeReleaseIndex(out, index, key, keys, now)
}

func readReleaseIndex(dir string, bootstrap bool, keys []ed25519.PublicKey, now time.Time) (engine.VersionIndex, error) {
	body, err := os.ReadFile(filepath.Join(dir, "versions.json"))
	if err != nil {
		return engine.VersionIndex{}, err
	}
	signature, err := os.ReadFile(filepath.Join(dir, "versions.json.sig"))
	if err == nil {
		verified, err := engine.VerifyIndex(engine.SignedIndex{Body: body, Signature: signature}, now, keys)
		if err != nil {
			return engine.VersionIndex{}, fmt.Errorf("current public index: %w", err)
		}
		return verified.Policy(), nil
	}
	if !bootstrap || !errors.Is(err, os.ErrNotExist) {
		return engine.VersionIndex{}, fmt.Errorf("current public index signature: %w", err)
	}
	var legacy struct {
		Latest   string            `json:"latest"`
		Versions []string          `json:"versions"`
		Sources  map[string]string `json:"sources"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return engine.VersionIndex{}, fmt.Errorf("bootstrap requires the legacy unsigned documentation index: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return engine.VersionIndex{}, errors.New("legacy index contains trailing data")
	}
	if !stableIndexVersion(legacy.Latest) || len(legacy.Versions) == 0 {
		return engine.VersionIndex{}, errors.New("bootstrap requires the legacy unsigned documentation index")
	}
	return engine.VersionIndex{}, nil
}

func stableIndexVersion(version string) bool {
	return semver.IsValid(version) && semver.Canonical(version) == version && semver.Major(version) == "v0" && semver.Prerelease(version) == ""
}

func nextReleaseIndex(index engine.VersionIndex, body []byte, required string, now time.Time) (engine.VersionIndex, error) {
	if !stableIndexVersion(required) {
		return index, errors.New("required release must be a stable v0.X.Y")
	}
	if !index.PublishedAt.IsZero() && !now.After(index.PublishedAt) {
		return index, errors.New("refusing to roll back index publication time")
	}
	pages, err := decodeReleasePages(body)
	if err != nil {
		return index, err
	}
	seen := make(map[string]bool)
	for _, record := range index.Versions {
		seen[record.Version] = true
	}
	found := false
	for _, page := range pages {
		for _, release := range page {
			if release.Draft || release.Prerelease || !stableIndexVersion(release.Tag) {
				continue
			}
			if release.PublishedAt.IsZero() || release.PublishedAt.After(now) {
				return index, fmt.Errorf("invalid publication date for %s", release.Tag)
			}
			if release.Tag == required {
				found = true
			}
			if !seen[release.Tag] {
				index.Versions = append(index.Versions, engineid.VersionInfo{Version: release.Tag, PublishedAt: release.PublishedAt, SupportUntil: release.PublishedAt.AddDate(0, limits.ControllerCompatibilityMonths, 0)})
				seen[release.Tag] = true
			}
			if index.Latest == "" || semver.Compare(release.Tag, index.Latest) > 0 {
				index.Latest = release.Tag
			}
		}
	}
	if !found {
		return index, fmt.Errorf("%s is not a public stable GitHub release", required)
	}
	index.Format = engine.VersionIndexFormat
	index.PublishedAt = now
	return index, nil
}

func writeReleaseIndex(out string, index engine.VersionIndex, key ed25519.PrivateKey, keys []ed25519.PublicKey, now time.Time) error {
	body, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	signature, err := releaseauth.Sign(key, body)
	if err != nil {
		return err
	}
	if _, err := engine.VerifyIndex(engine.SignedIndex{Body: body, Signature: signature}, now, keys); err != nil {
		return fmt.Errorf("refusing an index the client would reject: %w", err)
	}
	if err := os.WriteFile(filepath.Join(out, "versions.json"), body, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "versions.json.sig"), signature, 0o600)
}
