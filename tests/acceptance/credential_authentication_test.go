package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// safety: these cells exercise the HTTP authorization boundary with committed-fact fakes.
// They do not claim SQLite/Postgres mutation, native transport or deployment coverage.
type credentialBoundary struct {
	snapshot      capability.AuthenticationSnapshot
	raw           string
	handled       int
	race          bool
	readErr       error
	bindingErr    error
	bindingRule   capability.BindingRule
	bindingTarget capability.Target
}

func newCredentialBoundary() *credentialBoundary {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	secret := sha256.Sum256([]byte("acceptance synthetic work bearer"))
	raw := "swk_" + base64.RawURLEncoding.EncodeToString(secret[:])
	root := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}, Capability: capability.Capability{
		Prefix: "root", Audience: capability.Controller, Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Team: "team",
		Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim},
	}}
	claim := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "claim"}, Capability: capability.Capability{
		Digest: sha256.Sum256([]byte(raw)), Prefix: "claim", Audience: capability.Controller, Subject: root.Capability.Subject, Team: "team",
		Binding:  capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 4, Holder: "runner", Attempt: 2},
		Grants:   []capability.Grant{capability.NodeResult, capability.NodeReport, capability.LogTail, capability.SourceRead},
		Issuance: capability.Award, Unit: capability.UnitNode, UnitID: "unit", Purpose: capability.PurposeWork, Parent: &root.Authority, ExpiresAt: &expires,
	}}
	return &credentialBoundary{raw: raw, snapshot: capability.AuthenticationSnapshot{
		Now: now, Authorities: []capability.AuthorityState{{Principal: claim}, {Principal: root}},
		Binding: capability.BindingState{Current: claim.Capability.Binding, LeaseExpiresAt: now.Add(time.Minute)},
	}}
}

func (b *credentialBoundary) ReadAuthentication(_ context.Context, lookup capability.CredentialLookup, _ capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	if b.readErr != nil {
		return capability.AuthenticationSnapshot{}, b.readErr
	}
	if p := b.snapshot.Authorities[0].Principal; lookup.Class == capability.CredentialPeer && p.Class == lookup.Class && lookup.Peer == p.Capability.Subject {
		return b.snapshot, nil
	}
	if lookup.Digest != b.snapshot.Authorities[0].Principal.Capability.Digest {
		return capability.AuthenticationSnapshot{}, &apierr.Error{Code: apierr.Unauthenticated, Message: "unknown credential"}
	}
	return b.snapshot, nil
}

func (b *credentialBoundary) CheckBinding(_ context.Context, _ capability.Principal, rule capability.BindingRule, target capability.Target) error {
	b.bindingRule, b.bindingTarget = rule, target
	return b.bindingErr
}

func (b *credentialBoundary) router(t *testing.T) http.Handler {
	t.Helper()
	router := routepolicy.New(routepolicy.Authorization{Authenticator: capability.NewAuthenticator(b), Bindings: b})
	for _, pattern := range []string{
		"/api/v1/runs/{run}/jobs/{job}/transition",
		"/api/v1/runs/{run}/jobs/{job}/log-manifest",
		"/api/v1/runs/{run}/source-credential",
	} {
		if err := router.Register(controllerwire.Endpoint{
			Method: "POST", Pattern: pattern, Authentication: capability.AuthCapability,
			ResolveTarget: func(_ context.Context, _ capability.Principal, target capability.Target) (capability.Target, error) {
				return target, nil
			},
			Decode: func(r *http.Request) (capability.RequestEvidence, error) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					return capability.RequestEvidence{}, err
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				digest := sha256.Sum256(body)
				return capability.RequestEvidence{Target: capability.Target{Team: "team", RunID: r.PathValue("run"), NodeID: r.PathValue("job")}, ResultDigest: hex.EncodeToString(digest[:])}, nil
			},
			Handle: func(w http.ResponseWriter, _ *http.Request, auth capability.Authorized) error {
				if b.race {
					return &apierr.Error{Code: apierr.LockHeld, Message: "claim changed during transaction"}
				}
				if auth.Binding.Generation != 4 {
					t.Fatal("request fence replaced capability fence")
				}
				b.handled++
				w.WriteHeader(http.StatusNoContent)
				return nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := router.Validate(); err != nil {
		t.Fatal(err)
	}
	return router
}

func (b *credentialBoundary) request(t *testing.T, path, body string, want int, code apierr.Code) {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	if b.raw != "" {
		r.Header.Set("Authorization", "Bearer "+b.raw)
	}
	r.Header.Set("X-Sparkwing-Generation", "99")
	r.Header.Set("X-Sparkwing-Holder", "another-runner")
	w := httptest.NewRecorder()
	before := b.handled
	b.router(t).ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("HTTP %d: %s; want %d", w.Code, w.Body.String(), want)
	}
	if code == "" {
		if b.handled != before+1 {
			t.Fatal("positive cell did not reach handler")
		}
		return
	}
	if b.handled != before {
		t.Fatal("refused cell reached handler")
	}
	var envelope apierr.Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.Code != code {
		t.Fatalf("response = %s, want %s", w.Body.String(), code)
	}
	if code == apierr.Forbidden && envelope.Error.Details["reason"] == nil {
		t.Fatal("forbidden response omitted reason")
	}
}

func TestCredentialHTTPAuthenticationBoundary(t *testing.T) {
	const path = "/api/v1/runs/run/jobs/job/transition"
	for _, tc := range []struct {
		name   string
		change func(*credentialBoundary)
		status int
		code   apierr.Code
	}{
		{"live", func(*credentialBoundary) {}, 204, ""},
		{"absent", func(b *credentialBoundary) { b.raw = "" }, 401, apierr.Unauthenticated},
		{"unknown", func(b *credentialBoundary) {
			b.readErr = &apierr.Error{Code: apierr.Unauthenticated, Message: "unknown"}
		}, 401, apierr.Unauthenticated},
		{"expired", func(b *credentialBoundary) {
			b.snapshot.Authorities[0].Principal.Capability.ExpiresAt = &b.snapshot.Now
		}, 401, apierr.Unauthenticated},
		{"revoked parent", func(b *credentialBoundary) {
			b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
		}, 401, apierr.Unauthenticated},
		{"membership removed", func(b *credentialBoundary) {
			b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
		}, 401, apierr.Unauthenticated},
		{"missing grant", func(b *credentialBoundary) { b.snapshot.Authorities[0].Principal.Capability.Grants = nil }, 403, apierr.Forbidden},
		{"other team", func(b *credentialBoundary) { b.snapshot.Authorities[0].Principal.Capability.Team = "other" }, 403, apierr.Forbidden},
		{"ended claim", func(b *credentialBoundary) { b.snapshot.Binding.FinishedAt = &b.snapshot.Now }, 401, apierr.Unauthenticated},
		{"stale generation", func(b *credentialBoundary) { b.snapshot.Binding.Current.Generation++ }, 401, apierr.Unauthenticated},
		{"transaction fence race", func(b *credentialBoundary) { b.race = true }, 409, apierr.LockHeld},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newCredentialBoundary()
			tc.change(b)
			b.request(t, path, `{"status":"success"}`, tc.status, tc.code)
		})
	}
	t.Run("wrong path job", func(t *testing.T) {
		newCredentialBoundary().request(t, "/api/v1/runs/run/jobs/sibling/transition", `{"status":"success"}`, 403, apierr.Forbidden)
	})
	t.Run("wrong audience", func(t *testing.T) {
		b := newCredentialBoundary()
		b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
		b.snapshot.Authorities[0].Principal.Capability.Digest = sha256.Sum256([]byte(b.raw))
		b.snapshot.Authorities[0].Principal.Capability.Audience = capability.NodeHost
		b.snapshot.Binding.ProcessAlive = true
		b.request(t, path, `{"status":"success"}`, 403, apierr.Forbidden)
	})
}

func TestCredentialHTTPTerminalGrace(t *testing.T) {
	const body = `{"status":"success"}`
	const path = "/api/v1/runs/run/jobs/job/transition"
	b := newCredentialBoundary()
	digest := sha256.Sum256([]byte(body))
	finish := b.snapshot.Now.Add(-time.Second)
	b.snapshot.Binding.FinishedAt = &finish
	b.snapshot.Binding.LeaseExpiresAt = finish
	b.snapshot.Binding.Result = &capability.RecordedResult{Actor: b.snapshot.Authorities[0].Principal.Authority, Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/transition", RunID: "run", NodeID: "job", Generation: 4, Attempt: 2, Digest: hex.EncodeToString(digest[:])}
	b.request(t, path, body, 204, "")
	b.request(t, path, `{"status":"failed"}`, 401, apierr.Unauthenticated)
	b.request(t, "/api/v1/runs/run/jobs/job/log-manifest", `{}`, 204, "")
	b.snapshot.Now = finish.Add(limits.LogSealGrace)
	b.request(t, "/api/v1/runs/run/jobs/job/log-manifest", `{}`, 401, apierr.Unauthenticated)
}

func TestCredentialHTTPCancelDrainsReporting(t *testing.T) {
	b := newCredentialBoundary()
	b.snapshot.Binding.RunCancelled = true
	b.request(t, "/api/v1/runs/run/source-credential", `{}`, 401, apierr.Unauthenticated)
	b.request(t, "/api/v1/runs/run/jobs/job/transition", `{"status":"cancelled"}`, 204, "")
	b.request(t, "/api/v1/runs/run/jobs/job/log-manifest", `{}`, 204, "")
}

func sessionReference(p capability.Principal) capability.AuthorityRef {
	return capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(p.Capability.Digest[:])}
}
