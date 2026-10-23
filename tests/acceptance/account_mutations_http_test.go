package acceptance_test

import (
	"context"
	"crypto/sha256"
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

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

var accountMutations = [][3]string{
	{"POST", "/api/v1/me/active-team", `{"slug":"next-team"}`},
	{"POST", "/api/v1/teams", `{"slug":"Next-Team","display_name":"Next"}`},
	{"POST", "/api/v1/invitations/invite/accept", ""},
	{"DELETE", "/api/v1/me/identities/github", ""},
}

type accountWrites struct {
	actors []capability.Authorized
	args   []string
	err    error
}

func (f *accountWrites) called(a capability.Authorized, arg string) error {
	f.actors = append(f.actors, a)
	f.args = append(f.args, arg)
	return f.err
}

func (f *accountWrites) SelectAccountTeam(_ context.Context, a capability.Authorized, team store.Team) error {
	return f.called(a, string(team))
}

func (f *accountWrites) CreateAccountTeam(_ context.Context, a capability.Authorized, info store.TeamInfo) (store.TeamInfo, error) {
	return info, f.called(a, string(info.Slug)+"/"+info.DisplayName)
}

func (f *accountWrites) AcceptAccountInvitation(_ context.Context, a capability.Authorized, id string) (store.InvitationAcceptance, error) {
	return store.InvitationAcceptance{Team: "next-team"}, f.called(a, id)
}

func (f *accountWrites) UnlinkAccountIdentity(_ context.Context, a capability.Authorized, provider string) (int64, error) {
	return 2, f.called(a, provider)
}

var _ controller.AccountWriter = (*store.Operator)(nil)

func TestAccountMutationsHTTPUseCommittedStoreAdapter(t *testing.T) {
	f := newSessionFlow(t)
	now := time.Now()
	member, err := f.store.AsOperator().ResolveSignIn(t.Context(), store.SignInProfile{Provider: store.ProviderGoogle, Subject: "real-member", Email: "real-member@example.test", EmailVerified: true}, store.SignUpConditions{})
	if err != nil {
		t.Fatal(err)
	}
	f.raw, f.csrf, _, err = f.store.AsOperator().CreateAccountSession(t.Context(), member.Account, member.PersonalTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at,linked) VALUES ('github','linked',?,'',0,?,1)`, member.Account.ID, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	owner, err := f.store.AsOperator().ResolveSignIn(t.Context(), store.SignInProfile{Provider: store.ProviderGoogle, Subject: "real-owner", Email: "real-owner@example.test", EmailVerified: true}, store.SignUpConditions{})
	if err != nil {
		t.Fatal(err)
	}
	ownerRaw, _, _, err := f.store.AsOperator().CreateAccountSession(t.Context(), owner.Account, owner.PersonalTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := capability.LookupRoute("POST", "/api/v1/team/invitations")
	auth, err := capability.NewAuthenticator(f.store.AsOperator()).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialSession, Raw: ownerRaw}, route)
	if err != nil {
		t.Fatal(err)
	}
	a, err := capability.Authorize(t.Context(), f.store.AsOperator(), route, auth, capability.Target{Team: string(owner.PersonalTeam)})
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := f.store.ForTeam(t.Context(), owner.PersonalTeam)
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := tenant.CreateAuthorizedInvitation(t.Context(), a, member.Account.Email, store.RoleEditor, "real-invitation")
	if err != nil {
		t.Fatal(err)
	}
	h := f.server.Handler()
	for _, action := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/teams", `{"slug":"next-team","display_name":"Next"}`, 201},
		{"POST", "/api/v1/me/active-team", `{"slug":"` + string(member.PersonalTeam) + `"}`, 204},
		{"POST", "/api/v1/invitations/" + invitation.ID + "/accept", "", 204},
		{"DELETE", "/api/v1/me/identities/github", "", 204},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, f.request(action.method, action.path, action.body))
		if w.Code != action.status {
			t.Fatalf("%s: %d %s", action.path, w.Code, w.Body.String())
		}
	}
	session, err := f.store.AsOperator().LookupSession(t.Context(), f.raw)
	if err != nil || session.Team != invitation.Team {
		t.Fatalf("session=%+v: %v", session, err)
	}
	account, err := f.store.AsOperator().Account(t.Context(), session.AccountID)
	if err != nil || account.ActiveTeam != invitation.Team {
		t.Fatalf("account=%+v: %v", account, err)
	}
}

func TestAccountMutationHTTPIdentityProperty(t *testing.T) {
	f := newSessionFlow(t)
	writes := &accountWrites{}
	h := f.server.WithAccountWriter(writes).Handler()
	digest := sha256.Sum256([]byte(f.raw))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	property := func(input []byte, header bool) bool {
		writes.actors, writes.args = nil, nil
		forged := hex.EncodeToString(input)
		for _, action := range accountMutations {
			r := f.request(action[0], action[1]+"?"+url.Values{"account": {forged}, "team": {forged}, "session_id": {forged}}.Encode(), action[2])
			r.Header.Set("X-Account-ID", forged)
			r.Header.Set("X-Team", forged)
			if header {
				r.Header.Set("Authorization", "Session "+f.raw)
			}
			before, expiry := f.state(t)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			after, expires := f.state(t)
			want := 204
			if action[1] == "/api/v1/teams" {
				want = 201
				var result struct{ Slug, DisplayName, Role string }
				if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Slug != "next-team" || result.Role != "owner" {
					return false
				}
			}
			if w.Code != want || before != after || expiry != expires || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), f.raw) {
				return false
			}
		}
		if len(writes.actors) != 4 || strings.Join(writes.args, ",") != "next-team,next-team/Next,invite,github" {
			return false
		}
		for _, a := range writes.actors {
			if a.Principal.Class != capability.CredentialSession || a.Principal.Authority != authority || a.Principal.Capability.Subject.ID != "member" || a.Principal.Capability.Team != "team" {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountMutationHTTPProofProperty(t *testing.T) {
	f := newSessionFlow(t)
	writes := &accountWrites{}
	h := f.server.WithAccountWriter(writes).Handler()
	property := func(input []byte, mode uint8) bool {
		for _, action := range accountMutations {
			r := f.request(action[0], action[1], action[2])
			want := apierr.Forbidden
			switch mode % 4 {
			case 0:
				r.Header.Set("X-CSRF-Token", hex.EncodeToString(input))
			case 1:
				r.Header.Set("Origin", "https://foreign.test")
			case 2:
				r.Header.Set("Authorization", "Bearer "+hex.EncodeToString(input))
				want = apierr.Unauthenticated
			case 3:
				r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: hex.EncodeToString(input)})
				want = apierr.Unauthenticated
			}
			before, expiry := f.state(t)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			after, expires := f.state(t)
			var result apierr.Envelope
			if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Error == nil || result.Error.Code != want || w.Code != apierr.Status(want) || before != after || expiry != expires || len(writes.actors) != 0 {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountMutationHTTPAuthorityRefusals(t *testing.T) {
	for _, action := range accountMutations {
		for _, kind := range []string{"bearer", "machine", "peer", "console", "claim", "dead", "backend"} {
			t.Run(action[1]+"/"+kind, func(t *testing.T) {
				b, writes := accountReadBoundary(), &accountWrites{}
				p := &b.snapshot.Authorities[0].Principal
				want := apierr.Forbidden
				switch kind {
				case "bearer", "machine":
					p.Class = capability.CredentialBearer
					p.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: p.Capability.Prefix}
					if kind == "machine" {
						p.Capability.Subject.Kind = capability.SubjectMachine
					}
				case "peer":
					p.Class, p.Authority = capability.CredentialPeer, capability.AuthorityRef{}
					p.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
				case "console":
					b = consoleBoundary()
				case "claim":
					b = newCredentialBoundary()
				case "dead":
					p.Capability.RevokedAt = &b.snapshot.Now
					want = apierr.Unauthenticated
				case "backend":
					b.readErr = &apierr.Error{Code: apierr.Unavailable, Message: "authentication unavailable"}
					want = apierr.Unavailable
				}
				h := controller.New(nil, nil).WithAuthorization(consoleAuthorization(b)).WithAccountWriter(writes).Handler()
				r := httptest.NewRequest(action[0], action[1], strings.NewReader(action[2]))
				r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
				r.Header.Set("X-CSRF-Token", "session-csrf")
				if b.snapshot.Authorities[0].Principal.Class == capability.CredentialBearer {
					r.Header.Set("Authorization", "Bearer "+b.raw)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				assertEngineError(t, w, apierr.Status(want), want)
				if len(writes.actors) != 0 {
					t.Fatal("unrelated authority reached mutation")
				}
			})
		}
	}
}

func TestAccountMutationHTTPTransactionRefusals(t *testing.T) {
	f := newSessionFlow(t)
	writes := &accountWrites{}
	h := f.server.WithAccountWriter(writes).Handler()
	for _, tc := range []struct {
		err  error
		code apierr.Code
	}{
		{store.ErrNotMember, apierr.NotFound},
		{store.ErrNotFound, apierr.NotFound},
		{store.ErrSlugTaken, apierr.Conflict},
		{store.ErrEmailMismatch, apierr.Forbidden},
		{store.ErrInvitationClosed, apierr.Conflict},
		{store.ErrLastSignInMethod, apierr.Conflict},
		{&apierr.Error{Code: apierr.Forbidden, Message: "sign in again", Details: map[string]any{"reason": apierr.ReasonReauthRequired}}, apierr.Forbidden},
		{&apierr.Error{Code: apierr.Unauthenticated, Message: "session ended before commit"}, apierr.Unauthenticated},
		{errors.New("private-database-detail"), apierr.Internal},
	} {
		writes.err = tc.err
		for _, action := range accountMutations {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, f.request(action[0], action[1], action[2]))
			assertEngineError(t, w, apierr.Status(tc.code), tc.code)
			if strings.Contains(w.Body.String(), "private-") {
				t.Fatal("private transaction error returned")
			}
		}
	}
}

func TestAccountMutationHTTPInputAndMethods(t *testing.T) {
	f := newSessionFlow(t)
	writes := &accountWrites{}
	h := f.server.WithAccountWriter(writes).Handler()
	for _, path := range []string{"/api/v1/teams", "/api/v1/me/active-team"} {
		for _, body := range []string{`null`, `{}`, `{"slug":"next-team","account":"other"}`, `{"slug":"next-team"} {}`} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, f.request("POST", path, body))
			assertEngineError(t, w, 400, apierr.BadRequest)
		}
	}
	if len(writes.actors) != 0 {
		t.Fatal("invalid body reached mutation")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, f.request("DELETE", "/api/v1/me/identities/unknown", ""))
	assertEngineError(t, w, 404, apierr.NotFound)
	h = f.server.WithAccountWriter(nil).Handler()
	for _, action := range accountMutations {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, f.request("HEAD", action[1], ""))
		assertEngineError(t, w, 405, apierr.MethodNotAllowed)
	}
}
