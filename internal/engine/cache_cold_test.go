package engine

import (
	"bytes"
	"os"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

func TestColdCachedEngineRefusesModifiedAssets(t *testing.T) {
	r, transport, key, _, _ := fixtureResolver(t)
	ctx := t.Context()
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v0.10.2"}
	bundle := signedBundle(t, key, pin.Version, target, fixtureExecutable(t, pin.Version, target))
	transport.release(t, pin.Version, target, bundle)
	cold, err := r.Resolve(ctx, pin, target)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(cold.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(installed, bundle.Asset) {
		t.Fatal("installed bytes differ from the signed asset")
	}
	forgetVerified := func() {
		verifiedReleases.Lock()
		delete(verifiedReleases.entries, r.verificationKey(cold.Identity.Version, target, cold.Executable))
		verifiedReleases.Unlock()
	}
	defer forgetVerified()
	forgetVerified()
	if _, ok, err := r.cached(ctx, cold.Identity, target); err != nil || !ok {
		t.Fatalf("cold verification of intact cache failed: %v", err)
	}
	for name, body := range map[string][]byte{
		"grown":     append(append([]byte{}, installed...), 0),
		"truncated": installed[:len(installed)-1],
		"flipped":   func() []byte { b := append([]byte{}, installed...); b[len(b)/2] ^= 1; return b }(),
		"empty":     {},
	} {
		forgetVerified()
		if err := os.WriteFile(cold.Executable, body, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := r.cached(ctx, cold.Identity, target); err == nil || ok {
			t.Fatalf("%s: cold cache accepted modified engine", name)
		}
	}
	if err := os.WriteFile(cold.Executable, installed, 0o700); err != nil {
		t.Fatal(err)
	}
	forgetVerified()
	if _, ok, err := r.cached(ctx, cold.Identity, target); err != nil || !ok {
		t.Fatalf("restored cache refused: %v", err)
	}
}
