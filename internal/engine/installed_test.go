package engine

import (
	"context"
	"errors"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

func TestReplacementSelectsInstalledEngineWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "go.mod", []byte("module fixture\nrequire "+GoModule+" v0.0.0\nreplace "+GoModule+" => ../sdk\n"))
	pin, err := ReadPin(dir, engineid.GoSDK)
	if err != nil || pin.Replacement != "../sdk" || pin.Version != "v0.0.0" {
		t.Fatalf("replacement pin: %+v %v", pin, err)
	}
	r, err := NewResolver(Config{CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: installedTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("replacement consulted release transport")
		return nil, errors.New("unexpected download")
	})}})
	if err != nil {
		t.Fatal(err)
	}
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	selected, err := r.Resolve(t.Context(), pin, target)
	if err != nil {
		t.Fatal(err)
	}
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if selected.Executable != path || selected.Identity.Pin != pin || !validDigest(selected.AssetDigest) || selected.Identity.ManifestDigest != selected.AssetDigest {
		t.Fatalf("installed identity: %+v", selected)
	}
	if len(selected.Notices) != 1 || selected.Notices[0].Kind != engineid.NoticePinDisabled || !strings.Contains(selected.Notices[0].Reason, "engine pinning is off") {
		t.Fatalf("missing replacement warning: %+v", selected.Notices)
	}
	ensured, err := r.Ensure(t.Context(), selected.Identity, target)
	if err != nil || ensured.Identity != selected.Identity || ensured.Executable != selected.Executable {
		t.Fatalf("sealed replacement: %+v %v", ensured, err)
	}
	if err := selected.verifyInstalledEngine(); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Resolve(cancelled, pin, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled resolution: %v", err)
	}
}

func FuzzReplacementEngineCannotSubstituteAcceptedBinary(f *testing.F) {
	f.Add("foreign", "digest", "version", uint8(0))
	f.Add("", "", "", uint8(1))
	f.Fuzz(func(t *testing.T, executable, digest, version string, mutation uint8) {
		target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
		current, err := installedEngine(t.Context(), engineid.Pin{SDK: engineid.GoSDK, Version: "v0.0.0", Replacement: "../sdk"}, target)
		if err != nil {
			t.Fatal(err)
		}
		changed := current
		switch mutation % 6 {
		case 0:
			changed.Executable = executable
		case 1:
			changed.AssetDigest = digest
		case 2:
			changed.Identity.ManifestDigest = digest
		case 3:
			changed.Identity.Version = version
		case 4:
			changed.Target.GOOS = version
		case 5:
			changed.Target.GOARCH = executable
		}
		want := changed.Executable == current.Executable && changed.AssetDigest == current.AssetDigest && changed.Identity == current.Identity && changed.Target == current.Target
		if err := changed.verifyInstalledEngine(); (err == nil) != want {
			t.Fatalf("substituted installed engine: %+v: %v", changed, err)
		}
		_, err = (&Resolver{}).Ensure(t.Context(), changed.Identity, changed.Target)
		if (err == nil) != (changed.Identity == current.Identity && changed.Target == current.Target) {
			t.Fatalf("accepted engine substitution: %v", err)
		}
	})
}

type installedTransport func(*http.Request) (*http.Response, error)

func (f installedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLocalTypeScriptSDKSelectsInstalledEngineWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	localSDK(t, dir, "0.70.0")
	writeFixture(t, dir, "package.json", []byte(`{"dependencies":{"@sparkwing/sdk":"file:sdk.tgz"}}`))
	writeFixture(t, dir, "package-lock.json", []byte(`{"lockfileVersion":3,"packages":{"":{"dependencies":{"@sparkwing/sdk":"file:sdk.tgz"}},"node_modules/@sparkwing/sdk":{"version":"0.70.0","resolved":"file:sdk.tgz"}}}`))
	pin, err := ReadPin(dir, engineid.TypeScriptSDK)
	if err != nil || pin.Replacement != "sdk.tgz" {
		t.Fatalf("local pin=%+v error=%v", pin, err)
	}
	resolver, err := NewResolver(Config{CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: installedTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("local SDK downloaded a published engine")
		return nil, errors.New("unexpected download")
	})}})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := resolver.Resolve(t.Context(), pin, releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Identity.Pin != pin {
		t.Fatalf("selected pin=%+v", selected.Identity.Pin)
	}
	if err := selected.verifyInstalledEngine(); err != nil {
		t.Fatal(err)
	}
}

func TestReplacementEngineMismatchRemainsIntegrityFailure(t *testing.T) {
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	resolver, err := NewResolver(Config{CacheDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := resolver.Resolve(t.Context(), engineid.Pin{SDK: engineid.GoSDK, Version: "v0.0.0", Replacement: "/private/sdk-source", Manifest: "/private/go.mod"}, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{"version", "bytes"} {
		t.Run(changed, func(t *testing.T) {
			accepted := selected.Identity
			if changed == "version" {
				accepted.Version += "-previous"
			} else {
				accepted.ManifestDigest = strings.Repeat("0", 64)
			}
			_, err := resolver.Ensure(t.Context(), accepted, target)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.IntegrityFailure {
				t.Fatalf("changed installed engine must retain its integrity diagnosis: %v", err)
			}
			for _, want := range []string{
				"planned on engine " + accepted.Version,
				accepted.ManifestDigest,
				"this host has " + selected.Identity.Version,
				selected.Identity.ManifestDigest,
				"route planning to the executing host (repository runner requirements)",
				"or install " + accepted.Version,
			} {
				if !strings.Contains(failure.Message, want) {
					t.Errorf("mismatch message %q omits %q", failure.Message, want)
				}
			}
			if strings.Contains(failure.Message+failure.Hint, "/private/") {
				t.Errorf("mismatch exposed SDK paths: %v", failure)
			}
		})
	}
}
