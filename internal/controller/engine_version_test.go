package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type engineVersionFake struct {
	index         engine.IndexResult
	indexErr      error
	evaluateErr   error
	notices       []engineid.Notice
	evaluated     string
	indexTime     time.Time
	evaluatedTime time.Time
}

func (f *engineVersionFake) Index(_ context.Context, _ *engine.Resolver, now time.Time) (engine.IndexResult, error) {
	f.indexTime = now
	return f.index, f.indexErr
}

func (f *engineVersionFake) Evaluate(_ engine.VerifiedIndex, version string, now time.Time) ([]engineid.Notice, error) {
	f.evaluated, f.evaluatedTime = version, now
	return f.notices, f.evaluateErr
}

func engineVersionPolicy(f *engineVersionFake) *SignedEngineVersionPolicy {
	return &SignedEngineVersionPolicy{Resolver: &engine.Resolver{}, contracts: f}
}

func TestEngineVersionPolicyRefusesMissingOrFailedMetadata(t *testing.T) {
	failed := errors.New("signed index unavailable")
	for _, problem := range []string{"resolver", "index", "version"} {
		t.Run(problem, func(t *testing.T) {
			fake := &engineVersionFake{}
			policy := engineVersionPolicy(fake)
			version := "v1.2.3"
			switch problem {
			case "resolver":
				policy.Resolver = nil
			case "index":
				fake.indexErr = failed
			case "version":
				version = ""
			}
			if notices, err := policy.notices(t.Context(), version); err == nil || notices != nil || fake.evaluated != "" {
				t.Fatalf("unverified notices=%+v err=%v", notices, err)
			}
		})
	}
}

func TestEngineVersionNoticesPreserveSignedWarningsAndFallback(t *testing.T) {
	fallback := engineid.Notice{Kind: engineid.NoticeIndexFallback, Reason: "using verified cached index"}
	warning := engineid.Notice{Kind: engineid.NoticeSupportEnded, Version: "v1.2.3", Reason: "support window ended"}
	fake := &engineVersionFake{index: engine.IndexResult{Fallback: true, Notices: []engineid.Notice{fallback}}, notices: []engineid.Notice{warning}}
	got, err := engineVersionPolicy(fake).notices(t.Context(), "v1.2.3")
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
			got, err := engineVersionPolicy(fake).notices(t.Context(), "v1.2.3")
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

func TestCapabilitiesAlwaysDescribeDeployment(t *testing.T) {
	server := &Server{}
	for _, path := range []string{"/api/v1/capabilities", "/api/v1/capabilities?engine_version=v0.1.0"} {
		recorder := httptest.NewRecorder()
		server.handleCapabilities(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		var response capabilitiesResp
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK || !response.Claims.AllowRepos || !response.Claims.Profile {
			t.Fatalf("deployment capabilities at %s: %d %s", path, recorder.Code, recorder.Body.String())
		}
	}
}
