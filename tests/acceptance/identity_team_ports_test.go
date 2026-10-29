package acceptance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestAccountRolelessHTTPKeepsOnlyAccountAuthorityProperty(t *testing.T) {
	property := func(team []byte) bool {
		b, reads := accountReadBoundary(), &accountReads{}
		p := &b.snapshot.Authorities[0].Principal
		p.Capability.Team, p.Capability.Grants = "", nil
		h := accountReadServer(b, reads)
		w := discoveryRequest(h, b, "GET", "/api/v1/me")
		var result struct {
			User       struct{ ID string }
			ActiveTeam any `json:"active_team"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.User.ID != "member" || result.ActiveTeam != nil {
			t.Logf("account HTTP %d %s", w.Code, w.Body)
			return false
		}
		for _, path := range []string{"/api/v1/services", "/api/v1/team/members", "/api/v1/runners"} {
			r := httptest.NewRequest("GET", path, nil)
			r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
			r.Header.Set("X-Team", string(team))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Logf("%s: HTTP %d %s", path, w.Code, w.Body)
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountTeamHTTPTransactionGuards(t *testing.T) {
	f := newSessionFlow(t)
	writes := &accountWrites{}
	h := f.server.WithAccountWriter(writes).Handler()
	for _, tc := range []struct {
		path, body string
		err        error
		code       apierr.Code
	}{
		{"/api/v1/teams", `{"slug":"next-team"}`, store.ErrTeamLimit, apierr.Forbidden},
		{"/api/v1/teams", `{"slug":"next-team"}`, store.ErrSlugTaken, apierr.Conflict},
		{"/api/v1/teams", `{"slug":"next-team"}`, store.ErrInvalidSlug, apierr.BadRequest},
		{"/api/v1/invitations/invite/accept", "", store.ErrInvitationClosed, apierr.Conflict},
		{"/api/v1/invitations/missing/accept", "", store.ErrNotFound, apierr.NotFound},
	} {
		writes.err, writes.actors = tc.err, nil
		before, expiry := f.state(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, f.request("POST", tc.path, tc.body))
		assertEngineError(t, w, apierr.Status(tc.code), tc.code)
		after, expires := f.state(t)
		if len(writes.actors) != 1 || writes.actors[0].Principal.Capability.Subject.ID != "member" || before != after || expiry != expires || strings.Contains(w.Body.String(), f.raw) {
			t.Fatalf("HTTP %d %s actors=%+v", w.Code, w.Body, writes.actors)
		}
	}
}

func TestAccountMembershipHTTPCurrentRoleProperty(t *testing.T) {
	property := func(role uint8, remove bool) bool {
		f, writes := newSessionFlow(t), &engineTransactions{}
		owner := role%3 == 0
		current := []store.Role{store.RoleOwner, store.RoleEditor, store.RoleReader}[role%3]
		if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `UPDATE memberships SET role=? WHERE account_id='member'`, current); err != nil {
			t.Fatal(err)
		}
		h := f.server.WithEngineController(fakeEngine(t, writes)).Handler()
		digest := sha256.Sum256([]byte(f.raw))
		authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
		method, body := "PATCH", `{"role":"editor"}`
		if remove {
			method, body = "DELETE", ""
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, f.request(method, "/api/v1/team/members/other", body))
		if !owner {
			return w.Code == 403 && writes.writes == 0
		}
		return w.Code == 204 && writes.writes == 1 && writes.authorityActor.Authority == authority && writes.membership.AccountID == "other"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 36}); err != nil {
		t.Fatal(err)
	}
}
