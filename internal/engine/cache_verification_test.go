package engine

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestCachedEngineRefusesRewriteWithRestoredModificationTime(t *testing.T) {
	r, transport, key, _, _ := fixtureResolver(t)
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v0.10.2"}
	body := fixtureExecutable(t, pin.Version, target)
	transport.release(t, pin.Version, target, signedBundle(t, key, pin.Version, target, body))
	selected, err := r.Resolve(t.Context(), pin, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Ensure(t.Context(), selected.Identity, target); err != nil {
		t.Fatal(err)
	}
	r, err = NewResolver(r.config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Ensure(t.Context(), selected.Identity, target); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(selected.Executable)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Clone(body)
	body[len(body)-1] ^= 1
	if err := os.WriteFile(selected.Executable, body, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(selected.Executable, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatal(err)
	}
	requests := len(transport.requests)
	_, err = r.Ensure(t.Context(), selected.Identity, target)
	requireCode(t, err, hosterr.IntegrityFailure)
	var failure *apierr.Error
	if !errors.As(err, &failure) || !strings.Contains(failure.Hint, filepath.Dir(selected.Executable)) || !strings.Contains(failure.Hint, "retry the original command") || !strings.Contains(failure.Hint, "download and verify") {
		t.Fatalf("missing cache recovery instructions: %v", err)
	}
	if len(transport.requests) != requests {
		t.Fatal("corrupted cache was silently replaced")
	}
	requireCode(t, selected.verifyInstalledEngine(t.Context()), hosterr.IntegrityFailure)
}

func TestCachedEngineVerificationCannotCrossTrustSets(t *testing.T) {
	r, transport, key, _, _ := fixtureResolver(t)
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v0.10.2"}
	transport.release(t, pin.Version, target, signedBundle(t, key, pin.Version, target, fixtureExecutable(t, pin.Version, target)))
	selected, err := r.Resolve(t.Context(), pin, target)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		t.Run("matching_trust", func(t *testing.T) {
			t.Parallel()
			peer, err := NewResolver(r.config)
			if err != nil {
				t.Fatal(err)
			}
			current, hit, err := peer.cached(t.Context(), selected.Identity, target)
			if err != nil || !hit || current.Identity != selected.Identity {
				t.Fatalf("concurrent verification reuse: hit=%t engine=%+v error=%v", hit, current.Identity, err)
			}
		})
	}
	config := r.config
	config.TrustedKeys = signingKeys(testSigningKey(t))
	other, err := NewResolver(config)
	if err != nil {
		t.Fatal(err)
	}
	_, hit, err := other.cached(t.Context(), selected.Identity, target)
	if hit {
		t.Fatal("verification under another trust set was accepted")
	}
	requireCode(t, err, hosterr.IntegrityFailure)
}
