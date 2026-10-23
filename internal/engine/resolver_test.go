package engine

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
)

type fixtureTransport struct {
	responses map[string][]byte
	requests  []string
	failure   bool
}

func (f *fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, r.URL.String())
	if f.failure {
		return nil, fmt.Errorf("fixture offline")
	}
	body, ok := f.responses[r.URL.String()]
	status := 200
	if !ok {
		status = 404
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

func (f *fixtureTransport) policy(t *testing.T, key ed25519.PrivateKey, p VersionIndex) {
	signed := signPolicy(t, key, p)
	f.responses[VersionIndexURL] = signed.Body
	f.responses[VersionIndexSignatureURL] = signed.Signature
}

func fixtureResolver(t *testing.T) (*Resolver, *fixtureTransport, ed25519.PrivateKey, VersionIndex, time.Time) {
	t.Helper()
	key := testSigningKey(t)
	policy, now := policyFixture()
	transport := &fixtureTransport{responses: map[string][]byte{}}
	transport.policy(t, key, policy)
	r, err := NewResolver(Config{TrustedKeys: signingKeys(key), CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	return r, transport, key, policy, now
}

func TestIndexCacheFallbackRollbackAndPersistence(t *testing.T) {
	r, transport, key, policy, now := fixtureResolver(t)
	ctx := context.Background()
	result, err := r.Index(ctx, now)
	if err != nil || result.Fallback {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := r.Index(ctx, now.Add(limits.VersionIndexRefresh-time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("cache made %d requests", len(transport.requests))
	}
	transport.failure = true
	result, err = r.Index(ctx, now.AddDate(3, 0, 0))
	if err != nil || !result.Fallback {
		t.Fatalf("old fallback: %+v %v", result, err)
	}
	replacement, err := NewResolver(r.config)
	if err != nil {
		t.Fatal(err)
	}
	result, err = replacement.Index(ctx, now.AddDate(3, 0, 0))
	if err != nil || !result.Fallback {
		t.Fatalf("restart fallback: %+v %v", result, err)
	}
	requests := len(transport.requests)
	result, err = r.Index(ctx, now.AddDate(3, 0, 0).Add(time.Second))
	if err != nil || !result.Fallback || len(transport.requests) != requests {
		t.Fatal("fallback refresh was not cached with its notice")
	}
	transport.failure = false
	policy.PublishedAt = now.Add(-time.Hour)
	transport.policy(t, key, policy)
	result, err = r.Index(ctx, now.AddDate(3, 0, 0).Add(time.Hour))
	if err != nil || !result.Fallback || !result.Index.Policy().PublishedAt.Equal(now) {
		t.Fatalf("rollback: %+v %v", result, err)
	}
	policy.PublishedAt = now
	policy.Latest = "v0.10.2"
	transport.policy(t, key, policy)
	result, err = r.Index(ctx, now.AddDate(3, 0, 0).Add(2*time.Hour))
	if err != nil || !result.Fallback {
		t.Fatalf("same-time mutation: %+v %v", result, err)
	}
	policy.Latest = "v0.20.0"
	transport.policy(t, key, policy)
	result, err = r.Index(ctx, now.AddDate(3, 0, 0).Add(3*time.Hour))
	if err != nil || result.Fallback {
		t.Fatalf("identical replay: %+v %v", result, err)
	}
	copy := result.Index.Policy()
	copy.Versions[0].Version = "mutated"
	if result.Index.Policy().Versions[0].Version == "mutated" {
		t.Fatal("policy copy aliases authority")
	}
}

func TestIndexFirstUseFailure(t *testing.T) {
	r, transport, _, _, now := fixtureResolver(t)
	transport.failure = true
	_, err := r.Index(context.Background(), now)
	requireCode(t, err, apierr.Unavailable)
}

func fixtureExecutable(t *testing.T, version string, target releaseasset.Target) []byte {
	return fixtureExecutableSource(t, version, target, "package main\nvar Version=\"dev\"\nfunc main(){println(Version)}\n")
}

func fixtureExecutableSource(t *testing.T, version string, target releaseasset.Target, source string) []byte {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cmd", string(target.Binary)), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, dir, "go.mod", []byte("module "+GoModule+"\ngo 1.26\n"))
	writeFixture(t, dir, filepath.Join("cmd", string(target.Binary), "main.go"), []byte(source))
	out := filepath.Join(dir, "fixture.bin")
	cmd := exec.Command("go", "build", "-p", "2", "-ldflags", "-X main.Version="+version, "-o", out, "./cmd/"+string(target.Binary))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS="+target.GOOS, "GOARCH="+target.GOARCH, "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=-buildvcs=false")
	if body, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, body)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func signedBundle(t *testing.T, key ed25519.PrivateKey, version string, target releaseasset.Target, asset []byte) ReleaseBundle {
	t.Helper()
	names, err := releaseasset.NamesForVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	targetName, err := target.NameForVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, name := range names {
		hash := digest([]byte(name))
		if name == targetName {
			hash = digest(asset)
		}
		lines = append(lines, hash+"  "+name)
	}
	manifest := []byte(strings.Join(lines, "\n") + "\n")
	return ReleaseBundle{Asset: asset, AssetSignature: ed25519.Sign(key, asset), Manifest: manifest, ManifestSignature: ed25519.Sign(key, manifest)}
}

func (f *fixtureTransport) release(t *testing.T, version string, target releaseasset.Target, bundle ReleaseBundle) {
	t.Helper()
	name, err := target.NameForVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	base := ReleaseBase + "/" + version + "/"
	for path, body := range map[string][]byte{"SHA256SUMS": bundle.Manifest, "SHA256SUMS.sig": bundle.ManifestSignature, name: bundle.Asset, name + ".sig": bundle.AssetSignature} {
		f.responses[base+path] = body
	}
}

func TestExactEngineCacheCorruptionAndPolicyEnforcement(t *testing.T) {
	r, transport, key, policy, now := fixtureResolver(t)
	ctx := context.Background()
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v0.10.2"}
	bundle := signedBundle(t, key, pin.Version, target, fixtureExecutable(t, pin.Version, target))
	transport.release(t, pin.Version, target, bundle)
	cold, err := r.Resolve(ctx, pin, target)
	if err != nil {
		t.Fatal(err)
	}
	if cold.Identity.Version != "v0.10.2" || cold.Identity.ManifestDigest != digest(bundle.Manifest) {
		t.Fatalf("wrong exact engine: %+v", cold)
	}
	requests := len(transport.requests)
	warm, err := r.Ensure(ctx, cold.Identity, target)
	if err != nil || warm.Executable != cold.Executable || len(transport.requests) != requests {
		t.Fatalf("cache miss: %+v %v", warm, err)
	}
	type cacheCorruption struct {
		name   string
		mutate func(string) error
	}
	corruptions := []cacheCorruption{
		{"bytes", func(path string) error { return os.WriteFile(path, []byte("corrupt"), 0o700) }},
		{"verification_record", func(path string) error {
			return os.WriteFile(filepath.Join(filepath.Dir(path), "verification.json"), []byte("corrupt"), 0o600)
		}},
		{"missing_verification_record", func(path string) error {
			return os.Remove(filepath.Join(filepath.Dir(path), "verification.json"))
		}},
		{"ambiguous_verification_record", func(path string) error {
			record := filepath.Join(filepath.Dir(path), "verification.json")
			body, err := os.ReadFile(record)
			if err != nil {
				return err
			}
			return os.WriteFile(record, append([]byte(`{"Asset":null,`), body[1:]...), 0o600)
		}},
		{"directory", func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Mkdir(path, 0o700)
		}},
	}
	if runtime.GOOS != "windows" {
		corruptions = append(corruptions, cacheCorruption{"permissions", func(path string) error { return os.Chmod(path, 0o600) }})
	}
	for _, corruption := range corruptions {
		t.Run(corruption.name, func(t *testing.T) {
			if err := corruption.mutate(cold.Executable); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := r.Cached(ctx, cold.Identity, target); err != nil || ok {
				t.Fatalf("corrupt cache accepted: hit=%t err=%v", ok, err)
			}
			requests := len(transport.requests)
			r.config.Mode = toolchain.ModeLocal
			_, err := r.Ensure(ctx, cold.Identity, target)
			requireCode(t, err, apierr.Unavailable)
			if len(transport.requests) != requests {
				t.Fatal("local mode fetched a corrupt engine")
			}
			r.config.Mode = toolchain.ModeAuto
			repaired, err := r.Ensure(ctx, cold.Identity, target)
			if err != nil || repaired.Identity != cold.Identity {
				t.Fatalf("repair changed accepted engine: %+v %v", repaired, err)
			}
			if _, ok, err := r.Cached(ctx, cold.Identity, target); err != nil || !ok {
				t.Fatalf("repaired cache missed: hit=%t err=%v", ok, err)
			}
		})
	}
	policy.PublishedAt = now.Add(time.Minute)
	policy.Revoked = []Restriction{{Version: pin.Version, Reason: "fixture security issue", Security: true, AnnouncedAt: now, EffectiveAt: now}}
	transport.policy(t, key, policy)
	r.checkedAt = time.Time{}
	if _, err := r.Resolve(ctx, pin, target); err == nil {
		t.Fatal("warm revoked engine accepted")
	} else {
		requireCode(t, err, apierr.VersionRevoked)
	}
	for _, minimum := range []bool{false, true} {
		policy.PublishedAt = policy.PublishedAt.Add(time.Minute)
		code := apierr.VersionRevoked
		if minimum {
			policy.Minimum = Restriction{Version: "v0.20.0", Reason: "security minimum", Security: true, AnnouncedAt: now, EffectiveAt: now}
			policy.Revoked = nil
			code = apierr.VersionUnsupported
		}
		transport.policy(t, key, policy)
		r.checkedAt = time.Time{}
		_, err := r.Resolve(ctx, pin, target)
		requireCode(t, err, code)
		fresh, err := NewResolver(Config{TrustedKeys: signingKeys(key), CacheDirectory: t.TempDir(), HTTPClient: r.config.HTTPClient})
		if err != nil {
			t.Fatal(err)
		}
		_, err = fresh.Resolve(ctx, pin, target)
		requireCode(t, err, code)
	}
	for _, request := range transport.requests {
		if !strings.HasPrefix(request, ReleaseBase) && request != VersionIndexURL && request != VersionIndexSignatureURL {
			t.Fatalf("unofficial fetch: %s", request)
		}
	}
}

func TestReleaseVerificationRejectsUntrustedBytesBeforeExecution(t *testing.T) {
	key := testSigningKey(t)
	version := "v0.10.2"
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	body := fixtureExecutable(t, version, target)
	bundle := signedBundle(t, key, version, target, body)
	if _, err := VerifyRelease(version, target, bundle, nil); err == nil {
		t.Fatal("release accepted without trusted keys")
	}
	if _, err := VerifyRelease(version, target, bundle, signingKeys(key)); err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyRelease(version, target, bundle, signingKeys(key))
	if err != nil {
		t.Fatal(err)
	}
	otherResolver, err := NewResolver(Config{CacheDirectory: t.TempDir(), TrustedKeys: signingKeys(testSigningKey(t))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherResolver.StoreVerified(t.Context(), engineid.Pin{SDK: engineid.GoSDK, Version: version}, verified); err == nil {
		t.Fatal("resolver stored a release verified under another trust root")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ReleaseBundle)
	}{
		{"missing signature", func(b *ReleaseBundle) { b.AssetSignature = nil }},
		{"wrong digest", func(b *ReleaseBundle) {
			b.Asset = append(bytes.Clone(b.Asset), 'x')
			b.AssetSignature = ed25519.Sign(key, b.Asset)
		}},
		{"extra asset", func(b *ReleaseBundle) {
			b.Manifest = append(bytes.Clone(b.Manifest), []byte(digest(nil)+"  extra\n")...)
			b.ManifestSignature = ed25519.Sign(key, b.Manifest)
		}},
		{"missing asset", func(b *ReleaseBundle) {
			b.Manifest = []byte(strings.SplitN(string(b.Manifest), "\n", 2)[1])
			b.ManifestSignature = ed25519.Sign(key, b.Manifest)
		}},
		{"untrusted root", func(b *ReleaseBundle) {
			_, other, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			b.ManifestSignature = ed25519.Sign(other, b.Manifest)
			b.AssetSignature = ed25519.Sign(other, b.Asset)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := bundle
			tc.mutate(&bad)
			if _, err := VerifyRelease(version, target, bad, signingKeys(key)); err == nil {
				t.Fatal("invalid release accepted")
			}
		})
	}
	if _, err := VerifyRelease("v0.20.0", target, bundle, signingKeys(key)); err == nil {
		t.Fatal("wrong embedded version accepted")
	}
	other := target
	if other.GOARCH == "amd64" {
		other.GOARCH = "arm64"
	} else {
		other.GOARCH = "amd64"
	}
	wrong := signedBundle(t, key, version, other, body)
	if _, err := VerifyRelease(version, other, wrong, signingKeys(key)); err == nil {
		t.Fatal("wrong embedded architecture accepted")
	}
}

func TestEveryEnginePlatformCanBeInspectedWithoutExecution(t *testing.T) {
	key := testSigningKey(t)
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(goos+"_"+arch, func(t *testing.T) {
				target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: goos, GOARCH: arch}
				body := fixtureExecutable(t, "v0.10.2-rc.1", target)
				bundle := signedBundle(t, key, "v0.10.2-rc.1", target, body)
				if _, err := VerifyRelease("v0.10.2-rc.1", target, bundle, signingKeys(key)); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestDecodePolicyRejectsDuplicateKeysInUnknownFields(t *testing.T) {
	key := testSigningKey(t)
	p, now := policyFixture()
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body[:len(body)-1], []byte(`,"optional":{"x":1,"x":2}}`)...)
	_, err = VerifyIndex(SignedIndex{Body: body, Signature: ed25519.Sign(key, body)}, now, signingKeys(key))
	if err == nil {
		t.Fatal("duplicate unknown field accepted")
	}
}

func TestResolverOwnsTrustAfterConstruction(t *testing.T) {
	key := testSigningKey(t)
	policy, now := policyFixture()
	transport := &fixtureTransport{responses: map[string][]byte{}}
	transport.policy(t, key, policy)
	keys := signingKeys(key)
	config := Config{CacheDirectory: t.TempDir(), TrustedKeys: keys, HTTPClient: &http.Client{Transport: transport}}
	resolver, err := NewResolver(config)
	if err != nil {
		t.Fatal(err)
	}
	keys[0][0] ^= 0xff
	keys[0] = signingKeys(testSigningKey(t))[0]
	if _, err := resolver.Index(t.Context(), now); err != nil {
		t.Fatalf("caller changed constructed resolver trust: %v", err)
	}
	other, err := NewResolver(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Index(t.Context(), now); err == nil {
		t.Fatal("resolver accepted network or cached policy signed by another trust root")
	}
}
