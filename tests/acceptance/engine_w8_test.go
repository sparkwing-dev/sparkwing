package acceptance_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

type engineTransportW8 struct {
	body, signature []byte
	offline         bool
	calls           int
}

func runnerVersionPolicyW8(t *testing.T, version string) *engine.Resolver {
	t.Helper()
	public, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	body, err := json.Marshal(engine.VersionIndex{Format: 1, PublishedAt: now, Latest: version, Versions: []engineid.VersionInfo{{Version: version, PublishedAt: now, SupportUntil: now.AddDate(0, 12, 0)}}})
	if err != nil {
		t.Fatal(err)
	}
	transport := &engineTransportW8{body: body, signature: ed25519.Sign(key, body)}
	resolver, err := engine.NewResolver(engine.Config{TrustedKeys: []ed25519.PublicKey{public}, CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func TestEngineW8IndexFailureComponents(t *testing.T) {
	public, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	published := now.AddDate(0, -9, 0)
	policy := engine.VersionIndex{
		Format: 1, PublishedAt: now, Latest: "v0.20.0",
		Versions: []engineid.VersionInfo{
			{Version: "v0.10.0", PublishedAt: published, SupportUntil: published.AddDate(0, 12, 0)},
			{Version: "v0.20.0", PublishedAt: now.Add(-time.Hour), SupportUntil: now.Add(-time.Hour).AddDate(0, 12, 0)},
		},
		Minimum: engine.Restriction{Version: "v0.10.0", Reason: "minimum policy fixture", Security: true, AnnouncedAt: now, EffectiveAt: now},
		Revoked: []engine.Restriction{{Version: "v0.10.0", Reason: "revocation fixture", Security: true, AnnouncedAt: now, EffectiveAt: now}},
	}
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"signature", "malformed", "unavailable"} {
		t.Run(failure, func(t *testing.T) {
			transport := &engineTransportW8{body: body, signature: ed25519.Sign(key, body)}
			config := engine.Config{TrustedKeys: []ed25519.PublicKey{public}, CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: transport}}
			resolver, err := engine.NewResolver(config)
			if err != nil {
				t.Fatal(err)
			}
			verified, err := resolver.Index(t.Context(), now)
			if err != nil || !reflect.DeepEqual(verified.Index.Policy(), policy) {
				t.Fatalf("verified fields: %v, %v", verified, err)
			}
			if _, err := resolver.Index(t.Context(), now.Add(limits.VersionIndexRefresh-time.Nanosecond)); err != nil {
				t.Fatal(err)
			}
			if transport.calls != 2 {
				t.Fatalf("short cache made %d calls", transport.calls)
			}
			switch failure {
			case "signature":
				transport.signature = []byte("unverified")
			case "malformed":
				transport.body = []byte(`{"format":1,"format":1}`)
				transport.signature = ed25519.Sign(key, transport.body)
			case "unavailable":
				transport.offline = true
			}
			fallback, err := resolver.Index(t.Context(), now.Add(limits.VersionIndexRefresh))
			if err != nil || !fallback.Fallback || !reflect.DeepEqual(fallback.Index.Policy(), policy) || transport.calls <= 2 {
				t.Fatalf("fallback: %+v %v", fallback, err)
			}
			restarted, err := engine.NewResolver(config)
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := restarted.Index(t.Context(), now.AddDate(2, 0, 0))
			if err != nil || !persisted.Fallback || !reflect.DeepEqual(persisted.Index.Policy(), policy) {
				t.Fatalf("persisted fallback: %+v %v", persisted, err)
			}
			if _, err := engine.Evaluate(persisted.Index, "v0.20.0", now.AddDate(2, 0, 0)); err != nil {
				t.Fatalf("fallback blocked permitted release: %v", err)
			}
			config.CacheDirectory = t.TempDir()
			empty, err := engine.NewResolver(config)
			if err != nil {
				t.Fatal(err)
			}
			_, err = empty.Index(t.Context(), now)
			var problem *apierr.Error
			if !errors.As(err, &problem) || problem.Code != apierr.Unavailable {
				t.Fatalf("first use: %v", err)
			}
		})
	}
}

func (f *engineTransportW8) RoundTrip(request *http.Request) (*http.Response, error) {
	f.calls++
	if f.offline {
		return nil, fmt.Errorf("fixture offline")
	}
	var body []byte
	switch request.URL.String() {
	case engine.VersionIndexURL:
		body = f.body
	case engine.VersionIndexSignatureURL:
		body = f.signature
	default:
		return nil, fmt.Errorf("unexpected origin %s", request.URL)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}

func TestEngineW8FallbackHonorsSharedMachineRevocation(t *testing.T) {
	public, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	for _, failure := range []string{"unavailable", "signed_replay"} {
		t.Run(failure, func(t *testing.T) {
			policy := engine.VersionIndex{Format: 1, PublishedAt: now, Latest: "v0.20.0", Versions: []engineid.VersionInfo{
				{Version: "v0.10.0", PublishedAt: now.AddDate(0, -9, 0), SupportUntil: now.AddDate(0, 3, 0)},
				{Version: "v0.20.0", PublishedAt: now, SupportUntil: now.AddDate(1, 0, 0)},
			}}
			body, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			transport := &engineTransportW8{body: body, signature: ed25519.Sign(key, body)}
			config := engine.Config{TrustedKeys: []ed25519.PublicKey{public}, CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: transport}}
			first, err := engine.NewResolver(config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := first.Index(t.Context(), now); err != nil {
				t.Fatal(err)
			}
			policy.PublishedAt = now.Add(time.Minute)
			policy.Revoked = []engine.Restriction{{Version: "v0.10.0", Reason: "shared machine security revocation", Security: true, AnnouncedAt: now, EffectiveAt: now}}
			transport.body, err = json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			transport.signature = ed25519.Sign(key, transport.body)
			second, err := engine.NewResolver(config)
			if err != nil {
				t.Fatal(err)
			}
			if result, err := second.Index(t.Context(), policy.PublishedAt); err != nil || result.Fallback {
				t.Fatalf("publish newer verified policy: %+v %v", result, err)
			}
			transport.offline = failure == "unavailable"
			transport.body, transport.signature = body, ed25519.Sign(key, body)
			refreshedAt := now.Add(limits.VersionIndexRefresh)
			result, err := first.Index(t.Context(), refreshedAt)
			if err != nil || !result.Fallback {
				t.Fatalf("refresh fallback: %+v %v", result, err)
			}
			_, err = engine.Evaluate(result.Index, "v0.10.0", refreshedAt)
			var problem *apierr.Error
			if !errors.As(err, &problem) || problem.Code != apierr.VersionRevoked {
				t.Fatalf("newer cached revocation was lost: %v", err)
			}
			if _, err := engine.Evaluate(result.Index, "v0.20.0", refreshedAt); err != nil {
				t.Fatalf("permitted release refused: %v", err)
			}
		})
	}
}

func TestEngineW8Components(t *testing.T) {
	public, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	policy := engine.VersionIndex{Format: 1, PublishedAt: now, Latest: "v0.20.0", Versions: []engineid.VersionInfo{
		{Version: "v0.10.0", PublishedAt: now.AddDate(-2, 0, 0), SupportUntil: now.AddDate(-1, 0, 0)},
		{Version: "v0.20.0", PublishedAt: now.AddDate(0, -1, 0), SupportUntil: now.AddDate(0, 11, 0)},
	}}
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	transport := &engineTransportW8{body: body, signature: ed25519.Sign(key, body)}
	resolver, err := engine.NewResolver(engine.Config{TrustedKeys: []ed25519.PublicKey{public}, CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("signed_index_cache_and_fallback", func(t *testing.T) {
		result, err := resolver.Index(context.Background(), now)
		if err != nil {
			t.Fatal(err)
		}
		if result.Fallback || result.Index.Policy().Latest != "v0.20.0" {
			t.Fatalf("invalid verified policy: %+v", result)
		}
		if _, err := resolver.Index(context.Background(), now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if transport.calls != 2 {
			t.Fatal("short cache fetched again")
		}
		transport.offline = true
		result, err = resolver.Index(context.Background(), now.AddDate(2, 0, 0))
		if err != nil || !result.Fallback || len(result.Notices) != 1 {
			t.Fatalf("verified fallback: %+v %v", result, err)
		}
		empty, err := engine.NewResolver(engine.Config{TrustedKeys: []ed25519.PublicKey{public}, CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: transport}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = empty.Index(context.Background(), now)
		var api *apierr.Error
		if !errors.As(err, &api) || api.Code != apierr.Unavailable {
			t.Fatalf("first use error: %v", err)
		}
	})
	t.Run("expiry_warns_security_revocation_blocks", func(t *testing.T) {
		verified, err := engine.VerifyIndex(engine.SignedIndex{Body: body, Signature: ed25519.Sign(key, body)}, now, []ed25519.PublicKey{public})
		if err != nil {
			t.Fatal(err)
		}
		notices, err := engine.Evaluate(verified, "v0.10.0", now)
		if err != nil || len(notices) != 1 || notices[0].Kind != engineid.NoticeSupportEnded {
			t.Fatalf("expiry refused: %+v %v", notices, err)
		}
		policy.Revoked = []engine.Restriction{{Version: "v0.10.0", Reason: "fixture security revocation", Security: true, AnnouncedAt: now, EffectiveAt: now}}
		revoked, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		verified, err = engine.VerifyIndex(engine.SignedIndex{Body: revoked, Signature: ed25519.Sign(key, revoked)}, now, []ed25519.PublicKey{public})
		if err != nil {
			t.Fatal(err)
		}
		_, err = engine.Evaluate(verified, "v0.10.0", now)
		var api *apierr.Error
		if !errors.As(err, &api) || api.Code != apierr.VersionRevoked || api.Hint != "sparkwing upgrade" {
			t.Fatalf("revocation error: %v", err)
		}
		_, err = engine.EvaluateRunner(verified, "v0.10.0", now)
		if !errors.As(err, &api) || api.Hint == "sparkwing upgrade" {
			t.Fatalf("runner repair hint: %v", err)
		}
	})
}
