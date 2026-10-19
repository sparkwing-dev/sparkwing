package acceptance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"net/http/httptest"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func derivedIssuanceSession(lifetime uint16) *credentialBoundary {
	b := issuanceSession()
	root := b.snapshot.Authorities[0]
	root.Principal.Class = capability.CredentialBearer
	root.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "login-token"}
	root.Principal.Capability.Prefix = "login-token"
	end := b.snapshot.Now.Add((time.Duration(lifetime) + 1) * time.Minute)
	root.Principal.Capability.ExpiresAt = &end
	b.snapshot.Authorities[0].Principal.Capability.Parent = &root.Principal.Authority
	sessionEnd := end.Add(24 * time.Hour)
	b.snapshot.Authorities[0].Principal.Capability.ExpiresAt = &sessionEnd
	b.snapshot.Authorities = append(b.snapshot.Authorities, root)
	return b
}

func TestDerivedSessionIssuancePreservesAncestryProperty(t *testing.T) {
	property := func(lifetime uint16) bool {
		b, issuer := derivedIssuanceSession(lifetime), &credentialIssuer{}
		w := authorityHTTP(issuanceServer(b, issuer), b, "POST", "/api/v1/tokens", `{"grants":["runs.read"]}`)
		if w.Code != 201 || len(issuer.provisions) != 0 || len(issuer.delegations) != 1 {
			t.Logf("HTTP %d %s provisions=%d delegations=%d", w.Code, w.Body.String(), len(issuer.provisions), len(issuer.delegations))
			return false
		}
		p, spec := b.snapshot.Authorities[0].Principal, issuer.delegations[0]
		return spec.Parent == p.Authority && spec.ExpiresAt != nil && spec.ExpiresAt.Equal(*b.snapshot.Authorities[1].Principal.Capability.ExpiresAt)
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTokenLoginDescendantEndsWithOriginalToken(t *testing.T) {
	f := newSessionFlow(t)
	root := loginCapability(t, f)
	expiry := time.Now().Add(time.Hour)
	limited, err := f.store.DelegateCapability(t.Context(), capability.DelegateSpec{
		Parent:   capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix},
		Audience: capability.Controller, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone}, ExpiresAt: &expiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := f.server.WithDashboard(controller.Dashboard{}).Handler()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, loginRequest(limited.Raw, "browser-proof", "/"))
	if w.Code != 303 {
		t.Fatalf("login: HTTP %d %s", w.Code, w.Body)
	}
	var raw string
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "__Host-sw_session" {
			raw = cookie.Value
		}
	}
	session, err := f.store.LookupSession(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	f.raw, f.csrf = raw, session.CSRFToken
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request("POST", "/api/v1/tokens", `{"grants":["runs.read"]}`))
	var minted struct{ Capability string }
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &minted) != nil {
		t.Fatalf("mint: HTTP %d %s", w.Code, w.Body)
	}
	child, err := f.store.LookupCapability(t.Context(), sha256.Sum256([]byte(minted.Capability)))
	digest := sha256.Sum256([]byte(raw))
	parent := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	if err != nil || child.Capability.Parent == nil || *child.Capability.Parent != parent || child.Capability.ExpiresAt == nil || child.Capability.ExpiresAt.After(expiry) {
		t.Fatalf("descendant=%+v, %v", child.Capability, err)
	}
	tenant, err := f.store.ForTeam(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: limited.Capability.Prefix}
	actor := store.Actor{Authority: ref, CapabilityPrefix: ref.ID}
	if err := tenant.RevokeAuthority(t.Context(), actor, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: limited.Capability.Prefix}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.LookupCapability(t.Context(), sha256.Sum256([]byte(minted.Capability))); err == nil {
		t.Fatal("descendant survived login token revocation")
	}
}
