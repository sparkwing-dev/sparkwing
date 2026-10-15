package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type sessionOriginFake struct{}

func (sessionOriginFake) AuthorizeProvision(context.Context, capability.AuthorityResolver, capability.ProvisionSpec) error {
	return nil
}

func TestSessionRoutePreservesCapabilityScopeProperty(t *testing.T) {
	engine, _ := engineRuntimeStore(t)
	s := engine.Store
	engineSQL(t, s, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
	engineSQL(t, s, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','owner',1)`)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	pub, priv := licensetest.NewKey(t)
	now := time.Now()
	terms := licensetest.Terms{Features: []string{license.FeatureMultiTeam}, IssuedTo: "test", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	server := New(s, nil).WithSecretsCipher(cipher).WithLicense(license.Resolve(licensetest.Sign(t, priv, terms), pub, now, nil))
	memberRaw, _, _, err := s.CreateAccountSession(t.Context(), store.Account{ID: "member"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(memberRaw))
	s.SetProvisionPolicy(sessionOriginFake{})
	all := []capability.Grant{capability.RunsRead, capability.LogsRead, capability.RunsWrite, capability.RunsControl, capability.ApprovalsWrite, capability.TeamAdmin}
	random := rand.New(rand.NewPCG(183, 550))
	handler := server.Handler()
	for range 16 {
		var grants []capability.Grant
		var expected []string
		for _, grant := range all {
			if random.IntN(2) == 0 {
				grants = append(grants, grant)
				expected = append(expected, string(grant))
			}
		}
		if len(grants) == 0 {
			grants, expected = []capability.Grant{capability.RunsRead}, []string{"runs.read"}
		}
		issued, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone}})
		if err != nil {
			t.Fatal(err)
		}
		raw, _, err := s.CreateCapabilitySession(t.Context(), issued.Capability.Prefix, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
		req.Header.Set("Authorization", "Session "+raw)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("session route: %d %s", rec.Code, rec.Body.String())
		}
		var response sessionResp
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(response.Scopes, expected) {
			t.Fatalf("controller widened session grants: %v -> %v", expected, response.Scopes)
		}
	}
}
