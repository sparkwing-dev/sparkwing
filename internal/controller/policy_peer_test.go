package controller

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

type peerBoundary struct{ lookup capability.CredentialLookup }

func (b *peerBoundary) ReadAuthentication(_ context.Context, lookup capability.CredentialLookup, _ capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	b.lookup = lookup
	if lookup.Class != capability.CredentialPeer {
		return capability.AuthenticationSnapshot{}, sessionUnavailable()
	}
	return capability.AuthenticationSnapshot{Now: time.Now().UTC(), Authorities: []capability.AuthorityState{{
		Principal: capability.Principal{Class: capability.CredentialPeer, Capability: capability.Capability{
			Subject: lookup.Peer, Team: "default", Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead},
		}},
	}}}, nil
}

func (*peerBoundary) CheckBinding(_ context.Context, _ capability.Principal, rule capability.BindingRule, _ capability.Target) error {
	if rule != capability.BindNone {
		return routepolicy.MissingEndpointPolicy()
	}
	return nil
}

func TestPolicyPeerIdentityProperty(t *testing.T) {
	s := New(newStoreForAuth(t), nil)
	b := &peerBoundary{}
	s.authorization.Authenticator = capability.NewAuthenticator(b)
	s.authorization.Bindings = b
	type verifiedPeerKey struct{}
	s.WithPeerIdentity(func(r *http.Request) capability.Subject {
		subject, _ := r.Context().Value(verifiedPeerKey{}).(capability.Subject)
		return subject
	})
	h := s.Handler()
	property := func(value []byte, mutation uint8) bool {
		id := "uid-or-sid:" + hex.EncodeToString(value)
		r := httptest.NewRequest("GET", "http://local/api/v1/auth/whoami", nil)
		r.Header.Set("X-Local-UID", id)
		subject := capability.Subject{Kind: capability.SubjectLocal, ID: id}
		status := 200
		switch mutation % 6 {
		case 0:
		case 1:
			subject.Kind, status = capability.SubjectAccount, 401
		case 2:
			subject.ID, status = "", 401
		case 3:
			r.Header.Set("Authorization", "Session foreign")
			status = 401
		case 4:
			r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: "foreign"})
			status = 401
		case 5:
			r.Header.Set("Authorization", "malformed")
			status = 401
		}
		r = r.WithContext(context.WithValue(r.Context(), verifiedPeerKey{}, subject))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			return false
		}
		if status == 200 {
			var out whoamiJSON
			return json.Unmarshal(w.Body.Bytes(), &out) == nil && out.Principal == id && out.Kind == capability.SubjectLocal && b.lookup.Peer == subject && b.lookup.Class == capability.CredentialPeer && b.lookup.Digest == ([32]byte{})
		}
		var failure apierr.Envelope
		return json.Unmarshal(w.Body.Bytes(), &failure) == nil && failure.Error != nil && failure.Error.Code == apierr.Unauthenticated
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/auth/whoami", nil)
	r.Header.Set("X-Local-UID", "1000")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 || strings.Contains(w.Body.String(), "1000") {
		t.Fatalf("unverified transport: HTTP %d %s", w.Code, w.Body.String())
	}
}
