package capability_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

type authenticationReader struct {
	snapshot capability.AuthenticationSnapshot
	err      error
	lookup   capability.CredentialLookup
	reads    int
}

func (r *authenticationReader) ReadAuthentication(_ context.Context, lookup capability.CredentialLookup, _ capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	r.lookup = lookup
	r.reads++
	return r.snapshot, r.err
}

func authenticationFixture() (*authenticationReader, capability.Presentation, capability.RoutePolicy) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	secret := sha256.Sum256([]byte("synthetic credential"))
	raw := "swk_" + base64.RawURLEncoding.EncodeToString(secret[:])
	root := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "machine"}, Capability: capability.Capability{
		Prefix: "machine", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Team: "a", Audience: capability.Controller,
		Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim}, IssuedAt: now.Add(-time.Hour),
	}}
	claim := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "work"}, Capability: capability.Capability{
		Digest: sha256.Sum256([]byte(raw)), Prefix: "work", Subject: root.Capability.Subject, Team: "a", Audience: capability.Controller,
		Grants: []capability.Grant{capability.NodeResult, capability.LogTail, capability.SourceRead}, Issuance: capability.Award,
		Unit: capability.UnitNode, UnitID: "claim", Purpose: capability.PurposeWork, Delivery: capability.DeliveryDirect,
		Binding: capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 7, Holder: "runner", Attempt: 2},
		Parent:  &root.Authority, IssuedAt: now.Add(-time.Minute), ExpiresAt: &expires,
	}}
	reader := &authenticationReader{snapshot: capability.AuthenticationSnapshot{
		Now: now, Authorities: []capability.AuthorityState{{Principal: claim}, {Principal: root}},
		Binding: capability.BindingState{Current: claim.Capability.Binding, LeaseExpiresAt: now.Add(time.Minute)},
	}}
	route, _ := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/transition")
	return reader, capability.Presentation{Raw: raw, Class: capability.CredentialBearer, Evidence: capability.RequestEvidence{
		Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}, ResultDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}, route
}

func TestAuthenticationAncestry(t *testing.T) {
	tests := map[string]func(*authenticationReader){
		"leaf revoked": func(r *authenticationReader) {
			r.snapshot.Authorities[0].Principal.Capability.RevokedAt = &r.snapshot.Now
		},
		"parent revoked": func(r *authenticationReader) {
			r.snapshot.Authorities[1].Principal.Capability.RevokedAt = &r.snapshot.Now
		},
		"leaf expired": func(r *authenticationReader) {
			r.snapshot.Authorities[0].Principal.Capability.ExpiresAt = &r.snapshot.Now
		},
		"parent expired": func(r *authenticationReader) {
			r.snapshot.Authorities[1].Principal.Capability.ExpiresAt = &r.snapshot.Now
		},
		"membership removed": func(r *authenticationReader) {
			r.snapshot.Authorities[0].Principal.Capability.RevokedAt = &r.snapshot.Now
		},
		"parent membership removed": func(r *authenticationReader) {
			r.snapshot.Authorities[1].Principal.Capability.RevokedAt = &r.snapshot.Now
		},
		"missing parent": func(r *authenticationReader) { r.snapshot.Authorities = r.snapshot.Authorities[:1] },
		"wrong parent":   func(r *authenticationReader) { r.snapshot.Authorities[1].Principal.Authority.ID = "other" },
		"cycle": func(r *authenticationReader) {
			r.snapshot.Authorities[1].Principal.Capability.Parent = &r.snapshot.Authorities[0].Principal.Authority
		},
		"wrong digest": func(r *authenticationReader) { r.snapshot.Authorities[0].Principal.Capability.Digest = [32]byte{} },
		"wrong class": func(r *authenticationReader) {
			r.snapshot.Authorities[0].Principal.Class = capability.CredentialConsole
		},
		"unknown subject": func(r *authenticationReader) {
			r.snapshot.Authorities[0].Principal.Capability.Subject.Kind = "unknown"
		},
		"ended binding": func(r *authenticationReader) { r.snapshot.Binding.FinishedAt = &r.snapshot.Now },
		"expired lease": func(r *authenticationReader) { r.snapshot.Binding.LeaseExpiresAt = r.snapshot.Now },
		"reclaimed":     func(r *authenticationReader) { r.snapshot.Binding.Current.Generation++ },
		"new attempt":   func(r *authenticationReader) { r.snapshot.Binding.Current.Attempt++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r, p, route := authenticationFixture()
			mutate(r)
			_, err := capability.NewAuthenticator(r).Authenticate(t.Context(), p, route)
			requireAuthenticationCode(t, err, apierr.Unauthenticated)
		})
	}
	r, p, route := authenticationFixture()
	auth, err := capability.NewAuthenticator(r).Authenticate(t.Context(), p, route)
	if err != nil || len(auth.Live) != len(route.Access) || auth.Principal.Authority.ID != "work" {
		t.Fatalf("live authority = %+v, %v", auth, err)
	}
	if r.lookup.Digest != sha256.Sum256([]byte(p.Raw)) || r.lookup.Audience != capability.Controller {
		t.Fatalf("lookup did not use the complete reference digest: %+v", r.lookup)
	}
	// safety: launcher ancestry crosses teams while membership belongs to the awarded team.
	r.snapshot.Authorities[1].Principal.Capability.Team = "operator-team"
	if _, err := capability.NewAuthenticator(r).Authenticate(t.Context(), p, route); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticationTerminalExceptions(t *testing.T) {
	r, p, route := authenticationFixture()
	finish := r.snapshot.Now.Add(-time.Second)
	r.snapshot.Binding.FinishedAt = &finish
	r.snapshot.Binding.LeaseExpiresAt = finish
	r.snapshot.Binding.Result = &capability.RecordedResult{
		Actor: r.snapshot.Authorities[0].Principal.Authority, Method: route.Method, Pattern: route.Path,
		RunID: "run", NodeID: "job", Generation: 7, Attempt: 2, Digest: p.Evidence.ResultDigest,
	}
	authenticator := capability.NewAuthenticator(r)
	auth, err := authenticator.Authenticate(t.Context(), p, route)
	if err != nil || len(auth.Live) != 2 {
		t.Fatalf("replay = %+v, %v", auth, err)
	}
	for _, live := range auth.Live {
		if !live.ResultReplay {
			t.Fatalf("non-replay terminal access = %+v", live)
		}
	}
	p.Evidence.ResultDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err = authenticator.Authenticate(t.Context(), p, route)
	requireAuthenticationCode(t, err, apierr.Unauthenticated)
	p.Evidence.ResultDigest = r.snapshot.Binding.Result.Digest
	p.Evidence.Target.NodeID = "sibling"
	_, err = authenticator.Authenticate(t.Context(), p, route)
	requireAuthenticationCode(t, err, apierr.Unauthenticated)
	p.Evidence.Target.NodeID = "job"
	r.snapshot.Binding.Result.Actor.ID = "another actor"
	_, err = authenticator.Authenticate(t.Context(), p, route)
	requireAuthenticationCode(t, err, apierr.Unauthenticated)

	logRoute, _ := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/log-manifest")
	auth, err = authenticator.Authenticate(t.Context(), p, logRoute)
	if err != nil || len(auth.Live) == 0 || auth.Live[0].LogTailAttempt != 2 {
		t.Fatalf("tail = %+v, %v", auth, err)
	}
	r.snapshot.Now = finish.Add(limits.LogSealGrace)
	_, err = authenticator.Authenticate(t.Context(), p, logRoute)
	requireAuthenticationCode(t, err, apierr.Unauthenticated)
}

func TestAuthenticationCancelAndAlternativeIsolation(t *testing.T) {
	r, p, route := authenticationFixture()
	r.snapshot.Binding.RunCancelled = true
	if _, err := capability.NewAuthenticator(r).Authenticate(t.Context(), p, route); err != nil {
		t.Fatal(err)
	}
	source, _ := capability.LookupRoute("POST", "/api/v1/runs/{run}/source-credential")
	_, err := capability.NewAuthenticator(r).Authenticate(t.Context(), p, source)
	requireAuthenticationCode(t, err, apierr.Unauthenticated)
	route.Access = []capability.AccessRule{
		{Credential: capability.CredentialClaim, Grants: []capability.Grant{capability.SourceRead}, Binding: capability.BindSource, Liveness: capability.LiveSensitive},
		{Credential: capability.CredentialClaim, Grants: []capability.Grant{capability.NodeReport}, Binding: capability.BindOwnNode, Liveness: capability.LiveReporting},
	}
	auth, err := capability.NewAuthenticator(r).Authenticate(t.Context(), p, route)
	if err != nil || len(auth.Live) != 1 || auth.Live[0].RuleIndex != 1 {
		t.Fatalf("alternatives = %+v, %v", auth, err)
	}
	_, err = capability.Authorize(t.Context(), allowAuthenticationBinding{}, route, auth, p.Evidence.Target)
	requireAuthenticationCode(t, err, apierr.Forbidden)
}

type allowAuthenticationBinding struct{}

func (allowAuthenticationBinding) CheckBinding(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
	return nil
}

func requireAuthenticationCode(t *testing.T, err error, code apierr.Code) {
	t.Helper()
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func TestAuthenticationSessionLookupIncludesControllerAudience(t *testing.T) {
	for _, class := range []capability.CredentialClass{capability.CredentialSession, capability.CredentialConsole} {
		t.Run(string(class), func(t *testing.T) {
			reader, presentation, _ := authenticationFixture()
			presentation.Class = class
			digest := sha256.Sum256([]byte(presentation.Raw))
			principal := capability.Principal{Class: class, Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}, Capability: capability.Capability{
				Digest: sha256.Sum256([]byte(presentation.Raw)), Audience: capability.Controller, Team: "a",
				Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Binding: capability.Binding{Kind: capability.BindingNone},
			}}
			if class == capability.CredentialConsole {
				principal.Capability.Subject.Kind = capability.SubjectOperator
			}
			reader.snapshot.Authorities = []capability.AuthorityState{{Principal: principal}}
			route, ok := capability.LookupRoute("GET", "/api/v1/runs")
			if !ok {
				t.Fatal("run collection policy is absent")
			}
			if _, err := capability.NewAuthenticator(reader).Authenticate(t.Context(), presentation, route); err != nil {
				t.Fatal(err)
			}
			if reader.lookup.Audience != capability.Controller || reader.lookup.Digest != principal.Capability.Digest {
				t.Fatalf("session lookup = %+v", reader.lookup)
			}
		})
	}
}

func TestAuthenticationSessionMatchesOnlyItsCommittedRowProperty(t *testing.T) {
	property := func(value []byte, wrong bool) bool {
		reader, presentation, route := authenticationFixture()
		presentation.Class = capability.CredentialSession
		presentation.Raw = base64.RawURLEncoding.EncodeToString(append([]byte("session-"), value...))
		digest := sha256.Sum256([]byte(presentation.Raw))
		stored := digest
		if wrong {
			stored[0] ^= 1
		}
		reader.snapshot.Authorities = []capability.AuthorityState{{Principal: capability.Principal{
			Class: capability.CredentialSession, Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(stored[:])},
			Capability: capability.Capability{Digest: digest, Audience: capability.Controller, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Binding: capability.Binding{Kind: capability.BindingNone}},
		}}}
		_, err := capability.NewAuthenticator(reader).Authenticate(t.Context(), presentation, route)
		return (err == nil) != wrong
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerAuthorityUsesSupportedJobBindings(t *testing.T) {
	reader, presentation, _ := authenticationFixture()
	p := reader.snapshot.Authorities[0].Principal
	p.Capability.Unit, p.Capability.Purpose = capability.UnitTrigger, capability.PurposeTrigger
	p.Capability.Binding.Kind, p.Capability.Binding.NodeID, p.Capability.Binding.Attempt = capability.BindingTrigger, "", 0
	p.Capability.Grants = []capability.Grant{capability.NodeResult, capability.NodeReport, capability.LogTail}
	for _, suffix := range []string{"transition", "metrics", "usage", "activity", "steps/start", "steps/finish", "steps/skip", "steps/annotations", "steps/summary", "logs", "log-segments", "log-manifest"} {
		route, ok := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/"+suffix)
		if !ok {
			t.Fatalf("missing route %s", suffix)
		}
		auth := capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path}
		for i := range route.Access {
			auth.Live = append(auth.Live, capability.LiveAccess{RuleIndex: i})
		}
		authorized, err := capability.Authorize(t.Context(), allowAuthenticationBinding{}, route, auth, presentation.Evidence.Target)
		if suffix == "transition" || suffix == "metrics" || suffix == "usage" {
			if err != nil || authorized.Access.Binding != capability.BindRunOfTrigger {
				t.Fatalf("trigger %s: binding=%s err=%v", suffix, authorized.Access.Binding, err)
			}
		} else {
			requireAuthenticationCode(t, err, apierr.Forbidden)
		}
	}
}
