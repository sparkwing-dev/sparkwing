package engine

import (
	"context"
	"errors"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

func TestReplacementSelectsInstalledEngineWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "go.mod", []byte("module fixture\nrequire "+GoModule+" v0.0.0\nreplace "+GoModule+" => ../sdk\n"))
	pin, err := ReadPin(dir, GoSDK)
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
	if len(selected.Notices) != 1 || selected.Notices[0].Kind != NoticePinDisabled || !strings.Contains(selected.Notices[0].Reason, "engine pinning is off") {
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
		current, err := installedEngine(t.Context(), Pin{SDK: GoSDK, Version: "v0.0.0", Replacement: "../sdk"}, target)
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
