package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

func writeShipped(t *testing.T, root, version string, target releaseasset.Target, bundle ReleaseBundle) string {
	t.Helper()
	name, err := target.NameForVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, body := range map[string][]byte{"SHA256SUMS": bundle.Manifest, "SHA256SUMS.sig": bundle.ManifestSignature, name: bundle.Asset, name + ".sig": bundle.AssetSignature} {
		writeFixture(t, dir, file, body)
	}
	return filepath.Join(dir, name)
}

func releaseRequests(requests []string) []string {
	return slices.DeleteFunc(slices.Clone(requests), func(request string) bool { return !strings.HasPrefix(request, ReleaseBase) })
}

func shippedResolver(t *testing.T, r *Resolver, shipped string) *Resolver {
	t.Helper()
	config := r.config
	config.CacheDirectory = t.TempDir()
	config.ShippedDirectory = shipped
	next, err := NewResolver(config)
	if err != nil {
		t.Fatal(err)
	}
	next.installed = nil
	return next
}

func TestShippedReleaseResolvesWithoutDownloading(t *testing.T) {
	r, transport, key, _, _ := fixtureResolver(t)
	ctx := context.Background()
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v0.10.2"}
	asset := fixtureExecutable(t, pin.Version, target)
	bundle := signedBundle(t, key, pin.Version, target, asset)
	transport.release(t, pin.Version, target, bundle)
	shipped := t.TempDir()
	writeShipped(t, shipped, pin.Version, target, bundle)

	planning := shippedResolver(t, r, shipped)
	resolved, err := planning.Resolve(ctx, pin, target)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Identity.ManifestDigest != digest(bundle.Manifest) {
		t.Fatalf("wrong engine: %+v", resolved)
	}
	if stored, err := os.ReadFile(resolved.Executable); err != nil || !bytes.Equal(stored, asset) {
		t.Fatalf("cache does not hold the signed release bytes (err %v)", err)
	}
	if strings.HasPrefix(resolved.Executable, shipped) {
		t.Fatal("the engine runs from the shipped directory instead of the private cache")
	}
	restore := shippedResolver(t, r, shipped)
	restored, err := restore.Ensure(ctx, resolved.Identity, target)
	if err != nil || restored.Identity != resolved.Identity {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	if fetched := releaseRequests(transport.requests); len(fetched) != 0 {
		t.Fatalf("downloaded release files although the image ships them: %v", fetched)
	}
	if !slices.Contains(transport.requests, VersionIndexURL) {
		t.Fatal("resolved without reading the signed policy")
	}
}

func TestShippedReleaseFallsBackToTheDownload(t *testing.T) {
	r, transport, key, policy, now := fixtureResolver(t)
	ctx := context.Background()
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v0.10.2"}
	asset := fixtureExecutable(t, pin.Version, target)
	bundle := signedBundle(t, key, pin.Version, target, asset)
	transport.release(t, pin.Version, target, bundle)
	name, err := target.NameForVersion(pin.Version)
	if err != nil {
		t.Fatal(err)
	}
	assetURL := ReleaseBase + "/" + pin.Version + "/" + name
	other := fixtureExecutableSource(t, pin.Version, target, "package main\nvar Version=\"dev\"\nfunc main(){println(Version, 2)}\n")
	untrusted := testSigningKey(t)

	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{"missing_directory", func(t *testing.T, root string) {
			if err := os.Remove(root); err != nil {
				t.Fatal(err)
			}
		}},
		{"other_version_only", func(t *testing.T, root string) {
			writeShipped(t, root, "v0.20.0", target, signedBundle(t, key, "v0.20.0", target, fixtureExecutable(t, "v0.20.0", target)))
		}},
		{"corrupt_bytes", func(t *testing.T, root string) {
			executable := writeShipped(t, root, pin.Version, target, bundle)
			if err := os.WriteFile(executable, other, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"truncated_bytes", func(t *testing.T, root string) {
			executable := writeShipped(t, root, pin.Version, target, bundle)
			if err := os.WriteFile(executable, asset[:len(asset)/2], 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing_signature", func(t *testing.T, root string) {
			executable := writeShipped(t, root, pin.Version, target, bundle)
			if err := os.Remove(executable + ".sig"); err != nil {
				t.Fatal(err)
			}
		}},
		{"untrusted_signer", func(t *testing.T, root string) {
			writeShipped(t, root, pin.Version, target, signedBundle(t, untrusted, pin.Version, target, asset))
		}},
		{"symlinked_executable", func(t *testing.T, root string) {
			executable := writeShipped(t, root, pin.Version, target, bundle)
			elsewhere := filepath.Join(t.TempDir(), name)
			if err := os.Rename(executable, elsewhere); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, executable); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			shipped := t.TempDir()
			c.setup(t, shipped)
			transport.requests = nil
			resolved, err := shippedResolver(t, r, shipped).Resolve(ctx, pin, target)
			if err != nil {
				t.Fatal(err)
			}
			if stored, err := os.ReadFile(resolved.Executable); err != nil || !bytes.Equal(stored, asset) {
				t.Fatalf("cache does not hold the signed release bytes (err %v)", err)
			}
			if !slices.Contains(transport.requests, assetURL) {
				t.Fatal("resolved without downloading the signed release")
			}
		})
	}

	t.Run("differs_from_accepted_identity", func(t *testing.T) {
		// safety: both bundles carry valid signatures, but planning accepted the published manifest.
		shipped := t.TempDir()
		writeShipped(t, shipped, pin.Version, target, signedBundle(t, key, pin.Version, target, other))
		identity := engineid.Identity{Pin: pin, Version: pin.Version, ManifestDigest: digest(bundle.Manifest)}
		transport.requests = nil
		resolved, err := shippedResolver(t, r, shipped).Ensure(ctx, identity, target)
		if err != nil || resolved.Identity != identity {
			t.Fatalf("%+v %v", resolved, err)
		}
		if !slices.Contains(transport.requests, assetURL) {
			t.Fatal("accepted a shipped release whose manifest differs from the accepted identity")
		}
	})

	t.Run("corrupt_and_offline_fails_closed", func(t *testing.T) {
		shipped := t.TempDir()
		executable := writeShipped(t, shipped, pin.Version, target, bundle)
		if err := os.WriteFile(executable, other, 0o600); err != nil {
			t.Fatal(err)
		}
		identity := engineid.Identity{Pin: pin, Version: pin.Version, ManifestDigest: digest(bundle.Manifest)}
		resolver := shippedResolver(t, r, shipped)
		if _, err := resolver.sdkIndex(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		transport.requests = nil
		transport.failure = true
		defer func() { transport.failure = false }()
		if _, err := resolver.Ensure(ctx, identity, target); err == nil {
			t.Fatal("resolved an engine from corrupt shipped bytes while the download was unavailable")
		}
		if len(releaseRequests(transport.requests)) == 0 {
			t.Fatal("failed before trying the download")
		}
		if _, ok, err := resolver.cached(ctx, identity, target); ok || err != nil {
			t.Fatalf("corrupt shipped bytes reached the cache: hit=%t err=%v", ok, err)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		shipped := t.TempDir()
		writeShipped(t, shipped, pin.Version, target, bundle)
		revoked := policy
		revoked.PublishedAt = now.Add(time.Minute)
		revoked.Revoked = []Restriction{{Version: pin.Version, Reason: "fixture security issue", Security: true, AnnouncedAt: now, EffectiveAt: now}}
		transport.policy(t, key, revoked)
		defer transport.policy(t, key, policy)
		transport.requests = nil
		resolver := shippedResolver(t, r, shipped)
		_, err := resolver.Resolve(ctx, pin, target)
		requireCode(t, err, hosterr.VersionRevoked)
		_, err = resolver.Ensure(ctx, engineid.Identity{Pin: pin, Version: pin.Version, ManifestDigest: digest(bundle.Manifest)}, target)
		requireCode(t, err, hosterr.VersionRevoked)
		if fetched := releaseRequests(transport.requests); len(fetched) != 0 {
			t.Fatalf("a revoked engine read release files: %v", fetched)
		}
	})
}
