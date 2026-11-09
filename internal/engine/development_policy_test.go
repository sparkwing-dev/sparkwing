package engine

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/semver"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

func TestDevelopmentPolicySkipsIndexAndRunnerUpdates(t *testing.T) {
	transport := &fixtureTransport{failure: true}
	r, err := NewResolver(Config{BuildVersion: "(devel)", CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	index, err := r.Index(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, evaluate := range []func(VerifiedIndex, string, time.Time) ([]engineid.Notice, error){Evaluate, EvaluateRunner} {
		if notices, err := evaluate(index.Index, "(devel)", time.Now()); err != nil || len(notices) != 0 {
			t.Fatalf("development evaluation: %+v: %v", notices, err)
		}
	}
	update, err := r.PrepareRunnerUpdate(t.Context(), "(devel)", releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: "linux", GOARCH: "amd64"})
	if err != nil || update.Version != "(devel)" || update.Required || len(transport.requests) != 0 {
		t.Fatalf("development update fetched policy or replaced the runner: %+v: %v", update, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Index(ctx, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestMainBuildStampIsNarrow(t *testing.T) {
	for version, want := range map[string]bool{
		"v0.68.6-0-gabc1234": true, "v0.68.6-12-g0123456789abcdef0123456789abcdef01234567": true,
		"v0.68.6": false, "v0.68.6-dev+abc1234": false, "commit-0123456": false, "(devel)": false, "development": false,
		"v0.68.6-3-gABC1234": false, "v0.68.6-03-gabc1234": false, "v0.68.6-3-gabc12": false, "v0.68.6-3-gabc1234+dirty": false,
		"v0.68-3-gabc1234": false, "v0.68.6-qa.1-3-gabc1234": false,
	} {
		if mainBuild(version) != want {
			t.Errorf("mainBuild(%q) = %v, want %v", version, !want, want)
		}
	}
}

func TestMainBuildEnforcesSignedEnginePolicy(t *testing.T) {
	for _, version := range []string{"v0.20.0-3-gabc1234", "v0.20.1-0.20261106041046-718a7591f23a"} {
		t.Run(version, func(t *testing.T) {
			if err := RequirePolicyBuild("Cloud job", version, "", false); err != nil {
				t.Fatal(err)
			}
			fixture, transport, key, policy, now := fixtureResolver(t)
			policy.Revoked = []Restriction{{Version: "v0.10.2", Reason: "revocation fixture", Security: true, AnnouncedAt: now, EffectiveAt: now}}
			transport.policy(t, key, policy)
			config := fixture.config
			config.BuildVersion = version
			r, err := NewResolver(config)
			if err != nil {
				t.Fatal(err)
			}
			index, err := r.Index(t.Context(), now)
			if err != nil || index.Index.Development() || len(transport.requests) == 0 {
				t.Fatalf("main build skipped signed policy: %+v: %v", index, err)
			}
			if _, err := Evaluate(index.Index, "v0.20.0", now); err != nil {
				t.Fatalf("listed engine refused: %v", err)
			}
			_, err = Evaluate(index.Index, "v0.10.2", now)
			requireCode(t, err, hosterr.VersionRevoked)
			_, err = Evaluate(index.Index, "v0.30.0", now)
			requireCode(t, err, hosterr.VersionUnsupported)
			if _, err := Evaluate(index.Index, config.BuildVersion, now); err == nil {
				t.Fatal("unlisted service build accepted as a pinned engine")
			}
			config.CacheDirectory = t.TempDir()
			delete(transport.responses, VersionIndexSignatureURL)
			unsigned, err := NewResolver(config)
			if err != nil {
				t.Fatal(err)
			}
			_, err = unsigned.Index(t.Context(), now)
			requireCode(t, err, hosterr.Unavailable)
		})
	}
}

func TestRequirePolicyBuildAcceptsCanonicalPseudoVersion(t *testing.T) {
	const version = "v0.68.7-0.20261106041046-718a7591f23a"
	for _, component := range []string{"controller", "launcher", "Cloud job"} {
		if err := RequirePolicyBuild(component, version, "", false); err != nil {
			t.Fatalf("%s refused main build: %v", component, err)
		}
	}
	if semver.Compare("v0.68.6", version) >= 0 || semver.Compare(version, "v0.68.7") >= 0 {
		t.Fatal("main build must sort after its stable base and before the next release")
	}
}

func TestRequirePolicyBuildRefusesDevelopmentUnlessAllowed(t *testing.T) {
	for _, version := range []string{"", "v0.68.6", "v0.68.6-3-gabc1234"} {
		if err := RequirePolicyBuild("launcher", version, "X=1", false); err != nil {
			t.Fatalf("policy build %q refused: %v", version, err)
		}
	}
	for _, version := range []string{"commit-0123456", "v0.68.6-dev+abc1234", "(devel)"} {
		err := RequirePolicyBuild("launcher", version, "X=1", false)
		if err == nil || !strings.Contains(err.Error(), version) || !strings.Contains(err.Error(), "sparkwing run build-images") || !strings.Contains(err.Error(), "set X=1") {
			t.Fatalf("development build %q: %v", version, err)
		}
		if err := RequirePolicyBuild("launcher", version, "X=1", true); err != nil {
			t.Fatalf("explicit override refused %q: %v", version, err)
		}
	}
	if err := RequirePolicyBuild("Cloud job", "commit-0123456", "", false); err == nil || strings.Contains(err.Error(), "development cluster set") {
		t.Fatalf("job without an override: %v", err)
	}
}
