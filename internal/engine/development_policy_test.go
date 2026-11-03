package engine

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
	fixture, transport, key, policy, now := fixtureResolver(t)
	policy.Revoked = []Restriction{{Version: "v0.10.2", Reason: "revocation fixture", Security: true, AnnouncedAt: now, EffectiveAt: now}}
	transport.policy(t, key, policy)
	config := fixture.config
	config.BuildVersion = "v0.20.0-3-gabc1234"
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
	requireCode(t, err, apierr.VersionRevoked)
	_, err = Evaluate(index.Index, "v0.30.0", now)
	requireCode(t, err, apierr.VersionUnsupported)
	_, err = Evaluate(index.Index, config.BuildVersion, now)
	requireCode(t, err, apierr.VersionUnsupported)
}
