package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

type engineVersionFake struct {
	index         engine.IndexResult
	policy        engine.VersionIndex
	indexErr      error
	evaluateErr   error
	notices       []engineid.Notice
	release       engineid.VersionInfo
	evaluated     string
	indexTime     time.Time
	evaluatedTime time.Time
	compatibility controllerwire.Compatibility
}

func (f *engineVersionFake) Index(_ context.Context, _ *engine.Resolver, now time.Time) (engine.IndexResult, error) {
	f.indexTime = now
	return f.index, f.indexErr
}

func (f *engineVersionFake) Policy(engine.VerifiedIndex) engine.VersionIndex { return f.policy }

func (f *engineVersionFake) Evaluate(_ engine.VerifiedIndex, version string, now time.Time) ([]engineid.Notice, error) {
	f.evaluated, f.evaluatedTime = version, now
	return f.notices, f.evaluateErr
}

func (f *engineVersionFake) CompatibilityFor(release engineid.VersionInfo) controllerwire.Compatibility {
	f.release = release
	return f.compatibility
}

func engineVersionPolicy(f *engineVersionFake) *SignedEngineVersionPolicy {
	return &SignedEngineVersionPolicy{Resolver: &engine.Resolver{}, contracts: f}
}

func TestEngineCompatibilityUsesExactVerifiedRelease(t *testing.T) {
	published := time.Date(2024, time.February, 29, 12, 0, 0, 0, time.UTC)
	release := engineid.VersionInfo{Version: "v1.2.3", PublishedAt: published, SupportUntil: published.AddDate(1, 0, 0)}
	want := controllerwire.Compatibility{APIMajor: 1, EngineRelease: release.Version, ReleasedAt: published, GuaranteedUntil: release.SupportUntil}
	fake := &engineVersionFake{policy: engine.VersionIndex{Versions: []engineid.VersionInfo{{Version: "v9.0.0"}, release}, Revoked: []engine.Restriction{{Version: release.Version}}}, compatibility: want}
	server := EngineController{VersionPolicy: engineVersionPolicy(fake)}
	got, err := server.Compatibility(t.Context(), release.Version)
	if err != nil || got != want || fake.release != release || fake.evaluated != "" || fake.indexTime.IsZero() {
		t.Fatalf("compatibility=%+v release=%+v err=%v", got, fake.release, err)
	}
}

func TestEngineCompatibilityCannotSubstituteAnotherRelease(t *testing.T) {
	property := func(value string, known bool) bool {
		target := "v1.2.3"
		foreign := "foreign-" + value
		fake := &engineVersionFake{policy: engine.VersionIndex{Versions: []engineid.VersionInfo{{Version: foreign}}}}
		if known {
			fake.policy.Versions = append(fake.policy.Versions, engineid.VersionInfo{Version: target, PublishedAt: time.Unix(1, 0)})
			fake.compatibility = controllerwire.Compatibility{APIMajor: 1, EngineRelease: target}
		}
		got, err := (&EngineController{VersionPolicy: engineVersionPolicy(fake)}).Compatibility(t.Context(), target)
		if known {
			return err == nil && got.EngineRelease == target && fake.release.Version == target
		}
		var failure *apierr.Error
		return errors.As(err, &failure) && failure.Code == apierr.NotFound && got == (controllerwire.Compatibility{}) && fake.release.Version == ""
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestEngineVersionPolicyRefusesMissingOrFailedMetadata(t *testing.T) {
	failed := errors.New("signed index unavailable")
	for _, problem := range []string{"configuration", "resolver", "index", "version"} {
		t.Run(problem, func(t *testing.T) {
			fake := &engineVersionFake{policy: engine.VersionIndex{Versions: []engineid.VersionInfo{{Version: "v1.2.3"}}}}
			policy := engineVersionPolicy(fake)
			server := EngineController{VersionPolicy: policy}
			version := "v1.2.3"
			switch problem {
			case "configuration":
				server.VersionPolicy = nil
			case "resolver":
				policy.Resolver = nil
				server.VersionPolicy = policy
			case "index":
				fake.indexErr = failed
			case "version":
				version = ""
			}
			got, err := server.Compatibility(t.Context(), version)
			if err == nil || got != (controllerwire.Compatibility{}) || fake.release.Version != "" {
				t.Fatalf("compatibility=%+v release=%+v err=%v", got, fake.release, err)
			}
			if problem != "configuration" {
				if notices, err := policy.Notices(t.Context(), version); err == nil || notices != nil || fake.evaluated != "" {
					t.Fatalf("unverified notices=%+v err=%v", notices, err)
				}
			}
		})
	}
}

func TestEngineVersionNoticesPreserveSignedWarningsAndFallback(t *testing.T) {
	fallback := engineid.Notice{Kind: engineid.NoticeIndexFallback, Reason: "using verified cached index"}
	warning := engineid.Notice{Kind: engineid.NoticeSupportEnded, Version: "v1.2.3", Reason: "support window ended"}
	fake := &engineVersionFake{index: engine.IndexResult{Fallback: true, Notices: []engineid.Notice{fallback}}, notices: []engineid.Notice{warning}}
	got, err := engineVersionPolicy(fake).Notices(t.Context(), "v1.2.3")
	if err != nil || !reflect.DeepEqual(got, []engineid.Notice{fallback, warning}) || fake.evaluated != "v1.2.3" || !fake.indexTime.Equal(fake.evaluatedTime) {
		t.Fatalf("notices=%+v err=%v", got, err)
	}
	got[0].Reason, got[1].Reason = "changed", "changed"
	if fake.index.Notices[0] != fallback || fake.notices[0] != warning {
		t.Fatal("display response changed the engine owner's notice lists")
	}
}

func TestEngineVersionNoticesKeepEnforcementVisibleOnStoredRuns(t *testing.T) {
	for _, code := range []apierr.Code{apierr.VersionRevoked, apierr.VersionUnsupported, apierr.Unavailable, apierr.IntegrityFailure} {
		t.Run(string(code), func(t *testing.T) {
			failure := &apierr.Error{Code: code, Message: "signed reason", Hint: engine.UpgradeCommand, Details: map[string]any{"private": "omitted"}}
			fake := &engineVersionFake{evaluateErr: fmt.Errorf("evaluate: %w", failure)}
			got, err := engineVersionPolicy(fake).Notices(t.Context(), "v1.2.3")
			if code == apierr.VersionRevoked || code == apierr.VersionUnsupported {
				want := []engineid.Notice{{Kind: engineid.NoticeEnforcement, Version: "v1.2.3", Reason: failure.Message, Hint: failure.Hint}}
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("enforcement notices=%+v err=%v", got, err)
				}
			} else if !errors.Is(err, failure) || got != nil {
				t.Fatalf("policy failure notices=%+v err=%v", got, err)
			}
		})
	}
}
