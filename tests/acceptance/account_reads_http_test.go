package acceptance_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var accountReadPaths = []string{"/api/v1/me", "/api/v1/me/identities", "/api/v1/me/team-deletions"}

type accountReads struct {
	ids   []string
	email string
	err   error
}

func (f *accountReads) Account(_ context.Context, id string) (store.Account, error) {
	f.ids = append(f.ids, id)
	return store.Account{ID: id, Email: "member@example.test", ActiveTeam: "other"}, f.err
}

func (f *accountReads) AccountMemberships(_ context.Context, id string) ([]store.Membership, error) {
	f.ids = append(f.ids, id)
	return []store.Membership{{AccountID: id, Team: "team", Role: store.RoleEditor}}, f.err
}

func (f *accountReads) OpenInvitationsForEmail(_ context.Context, email string, _ time.Time) ([]store.Invitation, error) {
	f.email = email
	return []store.Invitation{{ID: "invite", Team: "inviting", Role: store.RoleReader}}, f.err
}

func (f *accountReads) AccountIdentities(_ context.Context, id string) ([]store.Identity, error) {
	f.ids = append(f.ids, id)
	return []store.Identity{{Provider: "github", Subject: "private-provider-subject", Email: "linked@example.test"}}, f.err
}

func (f *accountReads) TeamDeletionsRequestedBy(_ context.Context, id string) ([]store.TeamDeletion, error) {
	f.ids = append(f.ids, id)
	return []store.TeamDeletion{{Team: "deleted", State: "pending", LastError: "retry pending"}}, f.err
}

func accountReadBoundary() *credentialBoundary {
	b := authorityMember()
	p := &b.snapshot.Authorities[0].Principal
	p.Class, p.Authority = capability.CredentialSession, sessionReference(*p)
	p.Capability.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "member"}
	return b
}

func accountReadServer(b *credentialBoundary, f *accountReads) http.Handler {
	return controller.New(nil, nil).WithAuthorization(consoleAuthorization(b)).WithAccountReader(f).Handler()
}

func TestAccountReadHTTPIdentityProperty(t *testing.T) {
	property := func(input []byte) bool {
		b, f := accountReadBoundary(), &accountReads{}
		h := accountReadServer(b, f)
		forged := hex.EncodeToString(input)
		query := url.Values{"account": {forged}, "team": {forged}, "email": {forged}, "provider": {forged}}
		for _, path := range accountReadPaths {
			w := discoveryRequest(h, b, "GET", path+"?"+query.Encode())
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), b.raw) || strings.Contains(w.Body.String(), "private-") {
				return false
			}
			if path == "/api/v1/me" {
				var got struct {
					User       struct{ ID string }
					ActiveTeam struct{ Slug string } `json:"active_team"`
				}
				if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.User.ID != "member" || got.ActiveTeam.Slug != "team" {
					return false
				}
			}
		}
		for _, id := range f.ids {
			if id != "member" {
				return false
			}
		}
		return len(f.ids) == 4 && f.email == "member@example.test"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountReadHTTPRefusesUnrelatedAuthority(t *testing.T) {
	for _, path := range accountReadPaths {
		for _, kind := range []string{"unknown", "expired", "revoked", "removed", "bearer", "machine", "claim", "console", "peer", "backend"} {
			t.Run(path+"/"+kind, func(t *testing.T) {
				b, f := accountReadBoundary(), &accountReads{}
				p := &b.snapshot.Authorities[0].Principal
				code := apierr.Unauthenticated
				switch kind {
				case "unknown":
					b.readErr = &apierr.Error{Code: apierr.Unauthenticated, Message: "unknown credential"}
				case "expired":
					p.Capability.ExpiresAt = &b.snapshot.Now
				case "revoked":
					p.Capability.RevokedAt = &b.snapshot.Now
				case "removed":
					b.snapshot.Authorities[0].MembershipValid = false
				case "bearer":
					p.Class, code = capability.CredentialBearer, apierr.Forbidden
					p.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: p.Capability.Prefix}
				case "machine":
					p.Capability.Subject.Kind, p.Class, code = capability.SubjectMachine, capability.CredentialBearer, apierr.Forbidden
					p.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: p.Capability.Prefix}
				case "claim":
					b, code = newCredentialBoundary(), apierr.Forbidden
				case "console":
					b, code = consoleBoundary(), apierr.Forbidden
				case "peer":
					p.Class, p.Authority, code = capability.CredentialPeer, capability.AuthorityRef{}, apierr.Forbidden
					p.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
				case "backend":
					f.err, code = errors.New("private-backend-diagnostic"), apierr.Internal
				}
				w := discoveryRequest(accountReadServer(b, f), b, "GET", path)
				assertEngineError(t, w, apierr.Status(code), code)
				if kind != "backend" && len(f.ids) != 0 || strings.Contains(w.Body.String(), "private-") {
					t.Fatalf("reads=%v body=%s", f.ids, w.Body.String())
				}
			})
		}
	}
}

func TestAccountReadHTTPDoesNotRenewOrSwitchSession(t *testing.T) {
	f := newSessionFlow(t)
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE accounts SET active_team='team' WHERE id='member'`); err != nil {
		t.Fatal(err)
	}
	h := f.server.Handler()
	for _, path := range accountReadPaths {
		count, expires := f.state(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, f.request("GET", path+"?account=foreign&team=foreign", ""))
		gotCount, gotExpires := f.state(t)
		if w.Code != 200 || count != gotCount || expires != gotExpires || len(w.Result().Cookies()) != 0 {
			t.Fatalf("%s: HTTP %d %s state=%d/%d -> %d/%d", path, w.Code, w.Body.String(), count, expires, gotCount, gotExpires)
		}
		var account, team string
		if err := f.store.DB().QueryRowContext(t.Context(), `SELECT account_id,team FROM sessions`).Scan(&account, &team); err != nil || account != "member" || team != "team" {
			t.Fatalf("session=%s/%s, %v", account, team, err)
		}
		if path == "/api/v1/me" && !strings.Contains(w.Body.String(), `"id":"member"`) {
			t.Fatalf("account: %s", w.Body.String())
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, f.request("HEAD", path, ""))
		assertEngineError(t, w, 405, apierr.MethodNotAllowed)
	}
}

func TestCreditSettingsReadHTTP(t *testing.T) {
	f := newSessionFlow(t)
	h := f.server.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, f.request("GET", "/api/v1/credits/settings?team=foreign", ""))
	var got map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got["micro_per_credit"] == nil || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	b, reads := creditReadFixture()
	b.readErr = &apierr.Error{Code: apierr.Unauthenticated, Message: "unknown credential"}
	w = discoveryRequest(creditReadServer(t, b, reads), b, "GET", "/api/v1/credits/settings")
	assertEngineError(t, w, 401, apierr.Unauthenticated)
	b = accountReadBoundary()
	w = discoveryRequest(controller.New(nil, nil).WithAuthorization(consoleAuthorization(b)).Handler(), b, "GET", "/api/v1/credits/settings")
	assertEngineError(t, w, 404, apierr.NotFound)
}

func TestSelfServiceAccountDeletionIsGoneProperty(t *testing.T) {
	f := newSessionFlow(t)
	h := f.server.Handler()
	property := func(input []byte, bearer bool) bool {
		body, err := json.Marshal(map[string]string{"confirm_email": hex.EncodeToString(input)})
		if err != nil {
			return false
		}
		r := f.request("DELETE", "/api/v1/me", string(body))
		if bearer {
			r.Header.Set("Authorization", "Bearer "+hex.EncodeToString(input))
		}
		count, expires := f.state(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		after, expiry := f.state(t)
		var accountCount int
		err = f.store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM accounts WHERE id='member'`).Scan(&accountCount)
		var failure apierr.Envelope
		return err == nil && accountCount == 1 && count == after && expires == expiry &&
			w.Code == 405 && w.Header().Get("Allow") == "GET" && json.Unmarshal(w.Body.Bytes(), &failure) == nil &&
			failure.Error != nil && failure.Error.Code == apierr.MethodNotAllowed
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func TestConsoleAccountDeletionRetainsLastOwnerConflict(t *testing.T) {
	f := newSessionFlow(t)
	for _, statement := range []string{
		`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('target','target@example.test',1,1)`,
		`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('team','target','owner',1)`,
	} {
		if _, err := f.store.DB().ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	h := f.server.WithOperatorAccounts([]string{"member"}).WithConsoleWriter(consoleStoreFake{store: f.store}).Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, f.request("DELETE", "/api/v1/operator/accounts/target", ""))
	assertEngineError(t, w, 409, apierr.Conflict)
	var result struct {
		Error struct {
			Details struct {
				Teams []struct{ Slug string } `json:"teams"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Error.Details.Teams) != 1 || result.Error.Details.Teams[0].Slug != "team" {
		t.Fatalf("conflict: %s", w.Body.String())
	}
	if _, err := f.store.Account(t.Context(), "target"); err != nil {
		t.Fatalf("refused deletion changed account: %v", err)
	}
}
