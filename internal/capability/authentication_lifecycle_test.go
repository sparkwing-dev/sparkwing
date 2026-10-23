package capability

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestAuthenticationHereWindowAndSessionParent(t *testing.T) {
	r, p, _ := authenticationFixture()
	c := &r.snapshot.Authorities[0].Principal.Capability
	parent := &r.snapshot.Authorities[1].Principal
	parent.Class, parent.Authority.Kind = CredentialSession, AuthoritySession
	parent.Capability.Subject = Subject{Kind: SubjectAccount, ID: "account"}
	parent.Capability.Grants = []Grant{RunsWrite}
	c.Parent = &parent.Authority
	c.Unit, c.Purpose = UnitTrigger, PurposeHere
	claimBy := r.snapshot.Now.Add(time.Minute)
	c.Binding = Binding{Kind: BindingTrigger, RunID: "run", Reservation: "here:run", ClaimBy: &claimBy}
	r.snapshot.Binding.Current = c.Binding
	route, _ := LookupRoute("POST", "/api/v1/runs/{run}/claim")
	a := NewAuthenticator(r)
	if _, err := a.Authenticate(t.Context(), p, route); err != nil {
		t.Fatal(err)
	}
	discovery, _ := LookupRoute("GET", "/api/v1/services")
	if _, err := a.Authenticate(t.Context(), p, discovery); err != nil {
		t.Fatalf("preclaim discovery: %v", err)
	}
	source, _ := LookupRoute("POST", "/api/v1/runs/{run}/source-credential")
	_, err := a.Authenticate(t.Context(), p, source)
	requireAuthenticationCode(t, err, apierr.Unauthenticated)
	r.snapshot.Now = claimBy
	_, err = a.Authenticate(t.Context(), p, route)
	requireAuthenticationCode(t, err, apierr.Unauthenticated)
	r.snapshot.Binding.HereClaimed = true
	if _, err = a.Authenticate(t.Context(), p, route); err != nil {
		t.Fatalf("reclaim restarted first-claim window: %v", err)
	}
	r.snapshot.Binding.LeaseExpiresAt = r.snapshot.Now.Add(time.Minute)
	if _, err = a.Authenticate(t.Context(), p, source); err != nil {
		t.Fatal(err)
	}
	parent.Capability.Grants = nil
	_, err = a.Authenticate(t.Context(), p, route)
	requireAuthenticationCode(t, err, apierr.Unauthenticated)
}

func TestHereClaimStaysNamedAcrossReclaim(t *testing.T) {
	for _, state := range []string{"initial", "initial expired", "reclaim", "other run", "queue scan", "requester lost runs.write", "parent revoked", "expired"} {
		t.Run(state, func(t *testing.T) {
			r, presentation, _ := authenticationFixture()
			c := &r.snapshot.Authorities[0].Principal.Capability
			parent := &r.snapshot.Authorities[1].Principal
			parent.Class, parent.Authority.Kind = CredentialSession, AuthoritySession
			parent.Capability.Subject = Subject{Kind: SubjectAccount, ID: "account"}
			parent.Capability.Grants = []Grant{RunsWrite}
			c.Parent = &parent.Authority
			c.Unit, c.Purpose, c.Grants = UnitTrigger, PurposeHere, []Grant{TriggerHost}
			claimBy := r.snapshot.Now.Add(time.Minute)
			c.Binding = Binding{Kind: BindingTrigger, RunID: "run", Reservation: "here:run", ClaimBy: &claimBy}
			r.snapshot.Binding.Current = c.Binding
			path := "/api/v1/runs/{run}/claim"
			target := Target{Team: "a", RunID: "run"}
			code := apierr.Code("")
			switch state {
			case "initial expired":
				r.snapshot.Now = claimBy
				code = apierr.Unauthenticated
			case "reclaim":
				r.snapshot.Now = claimBy.Add(time.Minute)
				r.snapshot.Binding.HereClaimed = true
			case "other run":
				target.RunID = "other"
				code = apierr.Forbidden
			case "queue scan":
				path = "/api/v1/runners/claim"
				target.RunID = ""
				target.ClaimKinds = []UnitKind{UnitTrigger}
				code = apierr.Forbidden
			case "requester lost runs.write":
				parent.Capability.Grants = nil
				code = apierr.Unauthenticated
			case "parent revoked":
				parent.Capability.RevokedAt = &r.snapshot.Now
				code = apierr.Unauthenticated
			case "expired":
				c.ExpiresAt = &r.snapshot.Now
				code = apierr.Unauthenticated
			}
			route, ok := LookupRoute("POST", path)
			if !ok {
				t.Fatal("missing claim route")
			}
			authenticated, err := NewAuthenticator(r).Authenticate(t.Context(), presentation, route)
			if err == nil {
				_, err = Authorize(t.Context(), allowAuthenticationBinding{}, route, authenticated, target)
			}
			if code != "" {
				requireAuthenticationCode(t, err, code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !c.Binding.ClaimBy.Equal(claimBy) || c.Binding.Reservation != "here:run" {
				t.Fatal("claim window or reservation changed")
			}
		})
	}
}

func TestAuthenticationHereDescendantRetainsRequester(t *testing.T) {
	r, _, _ := authenticationFixture()
	here := r.snapshot.Authorities[0]
	here.Principal.Capability.Purpose = PurposeHere
	parent := r.snapshot.Authorities[1]
	parent.Principal.Capability.Grants = []Grant{RunsWrite}
	child := here
	child.Principal.Authority.ID = "narrowed"
	child.Principal.Capability.Prefix = "narrowed"
	child.Principal.Capability.Issuance = Delegate
	child.Principal.Capability.Parent = &here.Principal.Authority
	chain := []AuthorityState{child, here, parent}
	if _, err := ValidateAuthority(chain, r.snapshot.Now); err != nil {
		t.Fatal(err)
	}
	chain[2].Principal.Capability.Grants = nil
	_, err := ValidateAuthority(chain, r.snapshot.Now)
	requireAuthenticationCode(t, err, apierr.Unauthenticated)
}

func TestAuthenticationExpansionUsesOwnLease(t *testing.T) {
	tests := map[string]func(*authenticationReader){
		"source generation changed": func(r *authenticationReader) { r.snapshot.Binding.Current.Generation++ },
		"source is not terminal":    func(r *authenticationReader) { r.snapshot.Binding.FinishedAt = nil },
		"eval lease expired":        func(r *authenticationReader) { r.snapshot.Binding.Expansion.ExpiresAt = r.snapshot.Now },
		"eval holder changed":       func(r *authenticationReader) { r.snapshot.Binding.Expansion.Lease.Holder = "other" },
		"controller lease missing":  func(r *authenticationReader) { r.snapshot.Binding.Expansion.ControllerLeaseExpiresAt = nil },
		"controller lease expired":  func(r *authenticationReader) { r.snapshot.Binding.Expansion.ControllerLeaseExpiresAt = &r.snapshot.Now },
		"cancelled":                 func(r *authenticationReader) { r.snapshot.Binding.RunCancelled = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r, p, _ := authenticationFixture()
			c := &r.snapshot.Authorities[0].Principal.Capability
			c.Unit, c.Purpose = UnitEval, PurposeEval
			c.Binding.Expansion = &LeaseRef{Kind: UnitEval, ID: c.UnitID, Generation: 3, Holder: "eval-runner"}
			finish := r.snapshot.Now.Add(-time.Hour)
			r.snapshot.Binding.FinishedAt = &finish
			r.snapshot.Binding.LeaseExpiresAt = finish
			controllerExpires := r.snapshot.Now.Add(time.Minute)
			r.snapshot.Binding.Expansion = &ExpansionState{Lease: *c.Binding.Expansion, ExpiresAt: controllerExpires, ControllerLeaseExpiresAt: &controllerExpires}
			route, _ := LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/expansion")
			a := NewAuthenticator(r)
			if _, err := a.Authenticate(t.Context(), p, route); err != nil {
				t.Fatalf("live eval refused: %v", err)
			}
			c.Delivery, c.Grants = DeliveryBootstrap, []Grant{SourceRead, ClaimExchange}
			for _, path := range []string{"/api/v1/claims/exchange", "/api/v1/runs/{run}/source-credential"} {
				bootstrapRoute, _ := LookupRoute("POST", path)
				if _, err := a.Authenticate(t.Context(), p, bootstrapRoute); err != nil {
					t.Fatalf("live eval bootstrap refused on %s: %v", path, err)
				}
			}
			mutate(r)
			_, err := a.Authenticate(t.Context(), p, route)
			requireAuthenticationCode(t, err, apierr.Unauthenticated)
		})
	}
}

func TestAuthenticationProcessExitAndAudience(t *testing.T) {
	r, p, route := authenticationFixture()
	secret := sha256.Sum256([]byte("node process bearer"))
	p.Raw = "swn_" + base64.RawURLEncoding.EncodeToString(secret[:])
	c := &r.snapshot.Authorities[0].Principal.Capability
	c.Digest, c.Audience = sha256.Sum256([]byte(p.Raw)), NodeHost
	r.snapshot.Binding.ProcessAlive = true
	a := NewAuthenticator(r)
	auth, err := a.Authenticate(t.Context(), p, route)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Authorize(t.Context(), allowAuthenticationBinding{}, route, auth, p.Evidence.Target)
	requireAuthenticationCode(t, err, apierr.Forbidden)
	r.snapshot.Binding.ProcessAlive = false
	_, err = a.Authenticate(t.Context(), p, route)
	requireAuthenticationCode(t, err, apierr.Unauthenticated)
}

func TestAuthenticationRejectsMalformedPresentationsBeforeLookup(t *testing.T) {
	for _, raw := range []string{"", "swcg_old", "swk_short", "swk_" + string(make([]byte, 43)), "swk_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAB"} {
		t.Run(raw[:min(8, len(raw))], func(t *testing.T) {
			r, p, route := authenticationFixture()
			p.Raw = raw
			_, err := NewAuthenticator(r).Authenticate(t.Context(), p, route)
			requireAuthenticationCode(t, err, apierr.Unauthenticated)
			if r.reads != 0 {
				t.Fatal("malformed credential reached persistence")
			}
		})
	}
}

func TestAuthenticationPeerAndReadFailures(t *testing.T) {
	r, _, route := authenticationFixture()
	peer := Subject{Kind: SubjectLocal, ID: "uid:1000"}
	r.snapshot.Authorities = []AuthorityState{{Principal: Principal{
		Class: CredentialPeer, Capability: Capability{Subject: peer, Audience: Controller, Binding: Binding{Kind: BindingNone}},
	}}}
	p := Presentation{Class: CredentialPeer, Peer: peer}
	a := NewAuthenticator(r)
	if _, err := a.Authenticate(t.Context(), p, route); err != nil {
		t.Fatal(err)
	}
	p.Peer.ID = ""
	_, err := a.Authenticate(t.Context(), p, route)
	requireAuthenticationCode(t, err, apierr.Unauthenticated)
	p.Peer = peer
	r.err = errors.New("store unavailable")
	_, err = a.Authenticate(t.Context(), p, route)
	if !errors.Is(err, r.err) {
		t.Fatalf("read failure became auth failure: %v", err)
	}
}
