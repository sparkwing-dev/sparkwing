package capability

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

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

type authenticationReader struct {
	snapshot AuthenticationSnapshot
	err      error
	lookup   CredentialLookup
	reads    int
}

func (r *authenticationReader) ReadAuthentication(_ context.Context, lookup CredentialLookup, _ RequestEvidence) (AuthenticationSnapshot, error) {
	r.lookup = lookup
	r.reads++
	return r.snapshot, r.err
}

func authenticationFixture() (*authenticationReader, Presentation, RoutePolicy) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	secret := sha256.Sum256([]byte("synthetic credential"))
	raw := "swk_" + base64.RawURLEncoding.EncodeToString(secret[:])
	root := Principal{Class: CredentialBearer, Authority: AuthorityRef{Kind: AuthorityCapability, ID: "machine"}, Capability: Capability{
		Prefix: "machine", Subject: Subject{Kind: SubjectMachine, ID: "runner"}, Team: "a", Audience: Controller,
		Binding: Binding{Kind: BindingNone}, Grants: []Grant{NodesClaim}, IssuedAt: now.Add(-time.Hour),
	}}
	claim := Principal{Class: CredentialBearer, Authority: AuthorityRef{Kind: AuthorityCapability, ID: "work"}, Capability: Capability{
		Digest: sha256.Sum256([]byte(raw)), Prefix: "work", Subject: root.Capability.Subject, Team: "a", Audience: Controller,
		Grants: []Grant{NodeResult, LogTail, SourceRead}, Issuance: Award,
		Unit: UnitNode, UnitID: "claim", Purpose: PurposeWork, Delivery: DeliveryDirect,
		Binding: Binding{Kind: BindingNode, RunID: "run", NodeID: "job", Generation: 7, Holder: "runner", Attempt: 2},
		Parent:  &root.Authority, IssuedAt: now.Add(-time.Minute), ExpiresAt: &expires,
	}}
	reader := &authenticationReader{snapshot: AuthenticationSnapshot{
		Now: now, Authorities: []AuthorityState{{Principal: claim}, {Principal: root}},
		Binding: BindingState{Current: claim.Capability.Binding, LeaseExpiresAt: now.Add(time.Minute)},
	}}
	route, _ := LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/transition")
	return reader, Presentation{Raw: raw, Class: CredentialBearer, Evidence: RequestEvidence{
		Target: Target{Team: "a", RunID: "run", NodeID: "job"}, ResultDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
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
			r.snapshot.Authorities[0].Principal.Class = CredentialConsole
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
			_, err := NewAuthenticator(r).Authenticate(t.Context(), p, route)
			requireAuthenticationCode(t, err, apierr.Unauthenticated)
		})
	}
	r, p, route := authenticationFixture()
	auth, err := NewAuthenticator(r).Authenticate(t.Context(), p, route)
	if err != nil || len(auth.Live) != len(route.Access) || auth.Principal.Authority.ID != "work" {
		t.Fatalf("live authority = %+v, %v", auth, err)
	}
	if r.lookup.Digest != sha256.Sum256([]byte(p.Raw)) || r.lookup.Audience != Controller {
		t.Fatalf("lookup did not use the complete reference digest: %+v", r.lookup)
	}
	// safety: launcher ancestry crosses teams while membership belongs to the awarded team.
	r.snapshot.Authorities[1].Principal.Capability.Team = "operator-team"
	if _, err := NewAuthenticator(r).Authenticate(t.Context(), p, route); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticationTerminalExceptions(t *testing.T) {
	r, p, route := authenticationFixture()
	finish := r.snapshot.Now.Add(-time.Second)
	r.snapshot.Binding.FinishedAt = &finish
	r.snapshot.Binding.LeaseExpiresAt = finish
	r.snapshot.Binding.Result = &RecordedResult{
		Actor: r.snapshot.Authorities[0].Principal.Authority, Method: route.Method, Pattern: route.Path,
		RunID: "run", NodeID: "job", Generation: 7, Attempt: 2, Digest: p.Evidence.ResultDigest,
	}
	authenticator := NewAuthenticator(r)
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

	logRoute, _ := LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/log-manifest")
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
	if _, err := NewAuthenticator(r).Authenticate(t.Context(), p, route); err != nil {
		t.Fatal(err)
	}
	source, _ := LookupRoute("POST", "/api/v1/runs/{run}/source-credential")
	_, err := NewAuthenticator(r).Authenticate(t.Context(), p, source)
	requireAuthenticationCode(t, err, apierr.Unauthenticated)
	route.Access = []AccessRule{
		{Credential: CredentialClaim, Grants: []Grant{SourceRead}, Binding: BindSource, Liveness: LiveSensitive},
		{Credential: CredentialClaim, Grants: []Grant{NodeReport}, Binding: BindOwnNode, Liveness: LiveReporting},
	}
	auth, err := NewAuthenticator(r).Authenticate(t.Context(), p, route)
	if err != nil || len(auth.Live) != 1 || auth.Live[0].RuleIndex != 1 {
		t.Fatalf("alternatives = %+v, %v", auth, err)
	}
	_, err = Authorize(t.Context(), allowAuthenticationBinding{}, route, auth, p.Evidence.Target)
	requireAuthenticationCode(t, err, apierr.Forbidden)
}

type allowAuthenticationBinding struct{}

func (allowAuthenticationBinding) CheckBinding(context.Context, Principal, BindingRule, Target) error {
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
	for _, class := range []CredentialClass{CredentialSession, CredentialConsole} {
		t.Run(string(class), func(t *testing.T) {
			reader, presentation, _ := authenticationFixture()
			presentation.Class = class
			digest := sha256.Sum256([]byte(presentation.Raw))
			principal := Principal{Class: class, Authority: AuthorityRef{Kind: AuthoritySession, ID: hex.EncodeToString(digest[:])}, Capability: Capability{
				Digest: sha256.Sum256([]byte(presentation.Raw)), Audience: Controller, Team: "a",
				Subject: Subject{Kind: SubjectAccount, ID: "member"}, Binding: Binding{Kind: BindingNone},
			}}
			reader.snapshot.Authorities = []AuthorityState{{Principal: principal}}
			route, ok := LookupRoute("GET", "/api/v1/runs")
			if !ok {
				t.Fatal("run collection policy is absent")
			}
			if _, err := NewAuthenticator(reader).Authenticate(t.Context(), presentation, route); err != nil {
				t.Fatal(err)
			}
			if reader.lookup.Audience != Controller || reader.lookup.Digest != principal.Capability.Digest {
				t.Fatalf("session lookup = %+v", reader.lookup)
			}
		})
	}
}

func TestAuthenticationSessionMatchesOnlyItsCommittedRowProperty(t *testing.T) {
	property := func(value []byte, wrong bool) bool {
		reader, presentation, route := authenticationFixture()
		presentation.Class = CredentialSession
		presentation.Raw = base64.RawURLEncoding.EncodeToString(append([]byte("session-"), value...))
		digest := sha256.Sum256([]byte(presentation.Raw))
		stored := digest
		if wrong {
			stored[0] ^= 1
		}
		reader.snapshot.Authorities = []AuthorityState{{Principal: Principal{
			Class: CredentialSession, Authority: AuthorityRef{Kind: AuthoritySession, ID: hex.EncodeToString(stored[:])},
			Capability: Capability{Digest: digest, Audience: Controller, Subject: Subject{Kind: SubjectAccount, ID: "member"}, Binding: Binding{Kind: BindingNone}},
		}}}
		_, err := NewAuthenticator(reader).Authenticate(t.Context(), presentation, route)
		return (err == nil) != wrong
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerAuthorityUsesSupportedJobBindings(t *testing.T) {
	reader, presentation, _ := authenticationFixture()
	p := reader.snapshot.Authorities[0].Principal
	p.Capability.Unit, p.Capability.Purpose = UnitTrigger, PurposeTrigger
	p.Capability.Binding.Kind, p.Capability.Binding.NodeID, p.Capability.Binding.Attempt = BindingTrigger, "", 0
	p.Capability.Grants = []Grant{NodeResult, NodeReport, LogTail}
	for _, suffix := range []string{"transition", "metrics", "usage", "activity", "steps/start", "steps/finish", "steps/skip", "steps/annotations", "steps/summary", "logs", "log-segments", "log-manifest"} {
		route, ok := LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/"+suffix)
		if !ok {
			t.Fatalf("missing route %s", suffix)
		}
		auth := Authenticated{Principal: p, Method: route.Method, Pattern: route.Path}
		for i := range route.Access {
			auth.Live = append(auth.Live, LiveAccess{RuleIndex: i})
		}
		authorized, err := Authorize(t.Context(), allowAuthenticationBinding{}, route, auth, presentation.Evidence.Target)
		if suffix == "transition" || suffix == "metrics" || suffix == "usage" {
			if err != nil || authorized.Access.Binding != BindRunOfTrigger {
				t.Fatalf("trigger %s: binding=%s err=%v", suffix, authorized.Access.Binding, err)
			}
		} else {
			requireAuthenticationCode(t, err, apierr.Forbidden)
		}
	}
}

func TestCompletedOwnerAuthenticationAllowsOnlyFinalLogs(t *testing.T) {
	for _, kind := range []UnitKind{UnitTrigger, UnitPlan, UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			for _, change := range []string{"tail", "late", "sealed", "generation", "target", "revoked parent"} {
				t.Run(change, func(t *testing.T) {
					r, p, _ := authenticationFixture()
					c := &r.snapshot.Authorities[0].Principal.Capability
					c.Unit, c.UnitID, c.Binding.Attempt = kind, "claim", 0
					c.Grants = []Grant{LogTail, NodeReport}
					c.Binding.Kind = BindingRun
					if kind == UnitTrigger {
						c.Purpose, c.Binding.Kind = PurposeTrigger, BindingTrigger
					}
					if kind == UnitPlan {
						c.Purpose = PurposePlan
					}
					if kind == UnitEval {
						c.Purpose, c.Binding.Kind = PurposeEval, BindingNode
						c.Grants = []Grant{ExpansionSubmit}
						c.Binding.Expansion = &LeaseRef{Kind: UnitEval, ID: "claim", Holder: "eval", Generation: 9}
						r.snapshot.Binding.Expansion = &ExpansionState{Lease: *c.Binding.Expansion}
					}
					r.snapshot.Binding.Current = c.Binding
					finished := r.snapshot.Now.Add(-time.Second)
					r.snapshot.Binding.FinishedAt = &finished
					r.snapshot.Binding.LeaseExpiresAt = time.Time{}
					p.Evidence.Target = Target{Team: "a", RunID: "run", ClaimID: "claim"}
					switch change {
					case "late":
						r.snapshot.Now = finished.Add(limits.LogSealGrace)
					case "sealed":
						r.snapshot.Binding.LogSealed = true
					case "generation":
						r.snapshot.Binding.Current.Generation++
					case "target":
						p.Evidence.Target.ClaimID = "other-claim"
					case "revoked parent":
						r.snapshot.Authorities[1].Principal.Capability.RevokedAt = &r.snapshot.Now
					}
					for _, suffix := range []string{"log-segments", "log-manifest"} {
						route, _ := LookupRoute("POST", "/api/v1/claims/{claim}/"+suffix)
						_, err := NewAuthenticator(r).Authenticate(t.Context(), p, route)
						if (err == nil) != (change == "tail") {
							t.Fatalf("%s: %s authentication=%v", change, suffix, err)
						}
					}
					heartbeat, _ := LookupRoute("POST", "/api/v1/claims/{claim}/heartbeat")
					if _, err := NewAuthenticator(r).Authenticate(t.Context(), p, heartbeat); err == nil {
						t.Fatal("log grace reopened owner heartbeat")
					}
				})
			}
		})
	}
}
