package controller

import (
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
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestSessionRoutePreservesCapabilityScopeProperty(t *testing.T) {
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithSecretsCipher(cipher) })
	s := engine.Store
	engineSQL(t, s, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
	engineSQL(t, s, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','owner',1)`)
	server := engine.Server
	memberRaw, _, _, err := s.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(memberRaw))
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
		issued, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone}})
		if err != nil {
			t.Fatal(err)
		}
		raw, _, err := s.AsOperator().CreateCapabilitySession(t.Context(), issued.Capability.Prefix, time.Hour)
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

func TestMemberTokenHTTPSessionTimeoutAndLogout(t *testing.T) {
	for _, timeout := range []string{"idle", "absolute", "logout"} {
		t.Run(timeout, func(t *testing.T) {
			cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			engine, _ := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithSecretsCipher(cipher) })
			s := engine.Store
			handler := engine.Server.Handler()
			engineSQL(t, s, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
			engineSQL(t, s, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','editor',1)`)
			raw, csrf, _, err := s.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "a", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(raw))
			ref := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
			token, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &ref, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone}})
			if err != nil {
				t.Fatal(err)
			}
			child, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: token.Capability.Prefix}, Audience: capability.Controller, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone}})
			if err != nil {
				t.Fatal(err)
			}
			for _, issued := range []capability.Issued{token, child} {
				engineClaimHTTP(t, handler, issued.Raw, "GET", "/api/v1/runs/run/jobs", "", http.StatusOK)
			}
			if timeout == "logout" {
				request := httptest.NewRequest(http.MethodPost, "https://controller.test/api/v1/auth/logout", nil)
				request.Header.Set("Authorization", "Session "+raw)
				request.Header.Set("Origin", "https://controller.test")
				request.Header.Set(csrfHeaderName, csrf)
				request.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, engine.Server.cookiesSecure()), Value: csrf})
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusNoContent {
					t.Fatalf("logout: %d %s", response.Code, response.Body)
				}
			} else {
				if timeout == "idle" {
					engineSQL(t, s, `UPDATE sessions SET expires_at=? WHERE hash=?`, time.Now().Add(-time.Hour).UnixNano(), ref.ID)
				} else {
					engineSQL(t, s, `UPDATE sessions SET created_at=? WHERE hash=?`, time.Now().Add(-31*24*time.Hour).UnixNano(), ref.ID)
				}
				request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
				request.Header.Set("Authorization", "Session "+raw)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("expired session: %d %s", response.Code, response.Body)
				}
				for _, issued := range []capability.Issued{token, child} {
					engineClaimHTTP(t, handler, issued.Raw, "GET", "/api/v1/runs/run/jobs", "", http.StatusOK)
				}
				if _, err := s.AsOperator().DeleteSession(raw); err != nil {
					t.Fatal(err)
				}
			}
			for _, issued := range []capability.Issued{token, child} {
				engineClaimHTTP(t, handler, issued.Raw, "GET", "/api/v1/runs/run/jobs", "", http.StatusUnauthorized)
			}
		})
	}
}
