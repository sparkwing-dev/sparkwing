package main

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

const releasedVersions = `[[{"tag_name":"v0.67.0","published_at":"2026-10-01T00:00:00Z"},
 {"tag_name":"v0.68.2","published_at":"2026-10-31T01:55:48Z"},
 {"tag_name":"v0.68.1","draft":true}, {"tag_name":"v0.69.0-qa.1","prerelease":true}],
 [{"tag_name":"v0.66.5","published_at":"2026-09-01T00:00:00Z"}, {"tag_name":"v1.0.0"}]]`

func TestPublicIndexBackfillsPublishedStableReleases(t *testing.T) {
	now := time.Date(2026, 10, 31, 2, 0, 0, 0, time.UTC)
	index, err := nextReleaseIndex(engine.VersionIndex{}, []byte(releasedVersions), "v0.68.2", now)
	if err != nil {
		t.Fatal(err)
	}
	if index.Latest != "v0.68.2" || len(index.Versions) != 3 {
		t.Fatalf("index = %+v", index)
	}
	for _, v := range index.Versions {
		if !v.SupportUntil.Equal(v.PublishedAt.AddDate(0, 12, 0)) {
			t.Fatal("support window is not based on release date")
		}
	}
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	dir := t.TempDir()
	if err := writeReleaseIndex(dir, index, key, []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, now); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "versions.json"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join(dir, "versions.json.sig"))
	if err != nil {
		t.Fatal(err)
	}
	verified, err := engine.VerifyIndex(engine.SignedIndex{Body: body, Signature: sig}, now, []ed25519.PublicKey{key.Public().(ed25519.PublicKey)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Evaluate(verified, "v0.68.2", now); err != nil {
		t.Fatal(err)
	}
	body[0] ^= 1
	if _, err := engine.VerifyIndex(engine.SignedIndex{Body: body, Signature: sig}, now, []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}); err == nil {
		t.Fatal("accepted changed bytes")
	}
}

func TestPublicIndexRetainsPolicyAndDeletedReleaseHistory(t *testing.T) {
	now := time.Date(2026, 10, 31, 2, 0, 0, 0, time.UTC)
	published := now.AddDate(0, -2, 0)
	previous := engine.VersionIndex{Format: 1, PublishedAt: now.Add(-time.Hour), Latest: "v0.67.0",
		Minimum:  engine.Restriction{Version: "v0.67.0", Security: true, Reason: "security fix", AnnouncedAt: published, EffectiveAt: published},
		Revoked:  []engine.Restriction{{Version: "v0.66.0", Security: true, Reason: "unsafe", AnnouncedAt: published, EffectiveAt: published}},
		Versions: []engineid.VersionInfo{{Version: "v0.66.0", PublishedAt: published, SupportUntil: published.AddDate(0, 12, 0)}, {Version: "v0.67.0", PublishedAt: published, SupportUntil: published.AddDate(0, 12, 0)}}}
	index, err := nextReleaseIndex(previous, []byte(releasedVersions), "v0.68.2", now)
	if err != nil {
		t.Fatal(err)
	}
	if index.Minimum != previous.Minimum || index.Revoked[0] != previous.Revoked[0] || index.Versions[0] != previous.Versions[0] {
		t.Fatal("lost signed policy or release history")
	}
	if _, err := nextReleaseIndex(index, []byte(releasedVersions), "v0.67.0", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestPublicIndexFailsClosedOnIncompleteReleaseLookup(t *testing.T) {
	now := time.Now().UTC()
	for _, body := range []string{`[]`, `[null]`, `{}`, `[[]]`, `[[{"tag_name":"v0.68.2"}]]`, `[[{"tag_name":"v0.68.2","draft":true}]]`, `[[{"tag_name":"v0.68.2","published_at":"2099-01-01T00:00:00Z"}]]`} {
		if _, err := nextReleaseIndex(engine.VersionIndex{}, []byte(body), "v0.68.2", now); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestIndexBootstrapRefusesSignedOrPolicyShapedInput(t *testing.T) {
	dir := t.TempDir()
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	keys := []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}
	path := filepath.Join(dir, "versions.json")
	for _, body := range []string{`{"latest":"v0.67.0","versions":["v0.67.0"],"sources":{}}`, `{"format":1,"latest":"v0.67.0","versions":[]}`, `{"latest":"v0.67.0","versions":["v0.67.0"],"minimum":{}}`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := readReleaseIndex(dir, true, keys, time.Now())
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &fields); err != nil {
			t.Fatal(err)
		}
		if (err == nil) != (len(fields) == 3 && fields["sources"] != nil) {
			t.Fatalf("bootstrap accepted=%v for %s: %v", err == nil, body, err)
		}
		if _, err := readReleaseIndex(dir, false, keys, time.Now()); err == nil {
			t.Fatal("unsigned index accepted without bootstrap")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "versions.json.sig"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readReleaseIndex(dir, true, keys, time.Now()); err == nil {
		t.Fatal("bootstrap ignored an existing invalid signature")
	}
}
