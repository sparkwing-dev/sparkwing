package controller_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type teamDeletionBody struct {
	Slug      string `json:"slug"`
	State     string `json:"state"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`
}

// storageFake stands in for the logs and cache services, recording each
// delete it is asked for and answering with status.
type storageFake struct {
	mu       sync.Mutex
	status   int
	byPath   map[string]int
	requests []string
}

func (s *storageFake) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
	if status, ok := s.byPath[r.URL.Path]; ok {
		w.WriteHeader(status)
		return
	}
	w.WriteHeader(s.status)
}

func (s *storageFake) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *storageFake) answer(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

func newStorageFakes(t *testing.T) (logs, cache *storageFake, ts controller.TeamStorage) {
	t.Helper()
	logs, cache = &storageFake{status: http.StatusNoContent}, &storageFake{status: http.StatusNoContent}
	logSrv, cacheSrv := httptest.NewServer(http.HandlerFunc(logs.handler)), httptest.NewServer(http.HandlerFunc(cache.handler))
	t.Cleanup(logSrv.Close)
	t.Cleanup(cacheSrv.Close)
	return logs, cache, controller.TeamStorage{
		LogsURL: logSrv.URL, CacheURL: cacheSrv.URL, CacheToken: "cache-op",
	}
}

func newDeletionFixture(t *testing.T, ts controller.TeamStorage) *identityFixture {
	t.Helper()
	raw, pub := multiTeamLicense(t)
	f := newIdentityFixtureWith(t, fixtureOpts{license: raw, key: pub})
	token, _, err := f.store.CreateToken("controller-logs", store.TokenKindService,
		[]string{controller.ScopeLogsDelete}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	ts.LogsToken = token
	f.logsToken = token
	f.srv.WithTeamStorage(ts)
	return f
}

// createTeam makes a team owned by u and switches u's session to it.
func (f *identityFixture) createTeam(u *signedIn, slug string) {
	f.t.Helper()
	if code := f.call("POST", "/api/v1/teams", u.auth, map[string]string{"slug": slug, "display_name": slug}, nil); code != http.StatusCreated {
		f.t.Fatalf("create team %s = %d", slug, code)
	}
	u.team = slug
}

func (f *identityFixture) seedRun(team store.Team, id string) {
	f.t.Helper()
	tn, err := f.store.ForTeam(context.Background(), team)
	if err != nil {
		f.t.Fatal(err)
	}
	now := time.Now()
	if err := tn.CreateTriggerWithRun(context.Background(),
		store.Trigger{ID: id, Pipeline: "build", CreatedAt: now},
		store.Run{ID: id, Pipeline: "build", Status: "pending", StartedAt: now}); err != nil {
		f.t.Fatal(err)
	}
}

// afterCacheWindow is a pass time past the window in which another replica
// may still write through a tenant handle it cached before the request.
func afterCacheWindow() time.Time { return time.Now().Add(5 * time.Minute) }

func TestDeleteTeamClosesItAtOnceAndThePassRemovesItsStorage(t *testing.T) {
	logs, cache, ts := newStorageFakes(t)
	f := newDeletionFixture(t, ts)
	owner := f.user("o", "olga@example.com")
	personal := owner.team
	f.createTeam(&owner, "acme")
	editor := f.user("e", "eddie@example.com")
	f.join(owner, editor, "eddie@example.com", "editor")
	var minted struct {
		Token string `json:"token"`
	}
	if code := f.call("POST", "/api/v1/team/runner-tokens", owner.auth,
		map[string]any{"name": "box", "repos": []string{"github.com/acme/*"}}, &minted); code != http.StatusCreated {
		t.Fatalf("mint runner token = %d", code)
	}
	f.seedRun("acme", "run-acme")

	if code := f.call("DELETE", "/api/v1/team", editor.auth,
		map[string]string{"confirm_slug": "acme"}, nil); code != http.StatusForbidden {
		t.Fatalf("editor deleting the team = %d, want 403", code)
	}
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": personal}, nil); code != http.StatusBadRequest {
		t.Fatalf("a slug naming another team = %d, want 400", code)
	}
	var del teamDeletionBody
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, &del); code != http.StatusAccepted {
		t.Fatalf("owner deleting the team = %d, want 202", code)
	}
	if del.Slug != "acme" || del.State != store.TeamDeletionPending {
		t.Fatalf("deletion = %+v", del)
	}

	if code := f.call("GET", "/api/v1/auth/whoami", "Bearer "+minted.Token, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("the team's runner token after the request = %d, want 401", code)
	}
	var me meBody
	f.call("GET", "/api/v1/me", owner.auth, nil, &me)
	if me.ActiveTeam == nil || me.ActiveTeam.Slug != personal {
		t.Fatalf("owner's active team after deleting acme = %+v, want %s", me.ActiveTeam, personal)
	}
	for _, m := range me.Memberships {
		if m.Slug == "acme" {
			t.Fatal("the owner still lists the team being deleted")
		}
	}
	var em meBody
	f.call("GET", "/api/v1/me", editor.auth, nil, &em)
	if em.ActiveTeam == nil || em.ActiveTeam.Slug == "acme" {
		t.Fatalf("editor's active team after acme's deletion = %+v, want their own space", em.ActiveTeam)
	}

	f.srv.ProcessTeamDeletions(context.Background(), time.Now())
	if got := logs.seen(); len(got) != 0 {
		t.Fatalf("a pass inside the tenant-cache window deleted logs: %v", got)
	}
	f.srv.ProcessTeamDeletions(context.Background(), afterCacheWindow())
	if got := logs.seen(); len(got) != 1 || got[0] != "DELETE /api/v1/teams/acme/logs Bearer "+f.logsToken {
		t.Fatalf("logs service saw %v, want the team's logs deleted in one call", got)
	}
	if got := cache.seen(); len(got) != 1 || got[0] != "DELETE /admin/teams/acme Bearer cache-op" {
		t.Fatalf("cache service saw %v", got)
	}
	var mine []teamDeletionBody
	f.call("GET", "/api/v1/me/team-deletions", owner.auth, nil, &mine)
	if len(mine) != 1 || mine[0].State != store.TeamDeletionDone {
		t.Fatalf("owner's deletions = %+v, want acme done", mine)
	}
	if _, err := f.store.TeamInfo(context.Background(), "acme"); err == nil {
		t.Fatal("the team is still registered after the pass")
	}
}

// A logs service with no archive store answers the team route 404, and the
// purge then deletes the team's runs one at a time.
func TestTeamDeletionFallsBackToPerRunLogDeletesWithoutAnArchive(t *testing.T) {
	logs, _, ts := newStorageFakes(t)
	logs.byPath = map[string]int{"/api/v1/teams/acme/logs": http.StatusNotFound}
	f := newDeletionFixture(t, ts)
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	f.seedRun("acme", "run-acme")
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, nil); code != http.StatusAccepted {
		t.Fatalf("delete = %d", code)
	}
	f.srv.ProcessTeamDeletions(context.Background(), afterCacheWindow())
	want := []string{
		"DELETE /api/v1/teams/acme/logs Bearer " + f.logsToken,
		"DELETE /api/v1/logs/run-acme Bearer " + f.logsToken,
	}
	if got := logs.seen(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("logs service saw %v, want %v", got, want)
	}
	var mine []teamDeletionBody
	f.call("GET", "/api/v1/me/team-deletions", owner.auth, nil, &mine)
	if len(mine) != 1 || mine[0].State != store.TeamDeletionDone {
		t.Fatalf("deletion = %+v, want done", mine)
	}
}

func TestTeamDeletionWaitsOutAFailingLogsServiceAndThenFinishes(t *testing.T) {
	logs, _, ts := newStorageFakes(t)
	logs.answer(http.StatusBadGateway)
	f := newDeletionFixture(t, ts)
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	f.seedRun("acme", "run-acme")
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, nil); code != http.StatusAccepted {
		t.Fatalf("delete = %d", code)
	}

	f.srv.ProcessTeamDeletions(context.Background(), afterCacheWindow())
	var mine []teamDeletionBody
	f.call("GET", "/api/v1/me/team-deletions", owner.auth, nil, &mine)
	if len(mine) != 1 || mine[0].State != store.TeamDeletionPending || mine[0].Attempts != 1 || mine[0].LastError == "" {
		t.Fatalf("after a failed pass = %+v, want pending with the failure recorded", mine)
	}
	ids, err := f.store.AsOperator().TeamRunIDs(context.Background(), "acme")
	if err != nil || len(ids) != 1 {
		t.Fatalf("a failed storage purge removed rows: %v, %v", ids, err)
	}

	logs.answer(http.StatusNoContent)
	f.srv.ProcessTeamDeletions(context.Background(), afterCacheWindow())
	f.call("GET", "/api/v1/me/team-deletions", owner.auth, nil, &mine)
	if len(mine) != 1 || mine[0].State != store.TeamDeletionDone {
		t.Fatalf("after the service recovered = %+v, want done", mine)
	}
}

func TestDeleteTeamRefusesTheOnlyTeam(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": owner.team}, nil); code != http.StatusConflict {
		t.Fatalf("deleting the only team = %d, want 409", code)
	}
}

func TestDeleteAccountNeedsTheEmailAndNoOrphanedTeam(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	mia := f.user("m", "mia@example.com")
	f.join(owner, mia, "mia@example.com", "reader")

	if code := f.call("DELETE", "/api/v1/me", "Bearer "+f.admin, map[string]string{"confirm_email": "olga@example.com"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("a bearer token deleting an account = %d, want 401", code)
	}
	if code := f.call("DELETE", "/api/v1/me", owner.auth, map[string]string{"confirm_email": "mia@example.com"}, nil); code != http.StatusBadRequest {
		t.Fatalf("confirming with another address = %d, want 400", code)
	}
	var refusal struct {
		Teams []struct {
			Slug string `json:"slug"`
		} `json:"teams"`
	}
	if code := f.call("DELETE", "/api/v1/me", owner.auth, map[string]string{"confirm_email": "olga@example.com"}, &refusal); code != http.StatusConflict {
		t.Fatalf("last owner deleting their account = %d, want 409", code)
	}
	if len(refusal.Teams) != 1 || refusal.Teams[0].Slug != owner.team {
		t.Fatalf("refusal names %+v, want %s", refusal.Teams, owner.team)
	}

	var done struct {
		DeletedTeams []string `json:"deleted_teams"`
	}
	if code := f.call("DELETE", "/api/v1/me", mia.auth, map[string]string{"confirm_email": "Mia@Example.com"}, &done); code != http.StatusOK {
		t.Fatalf("member deleting their account = %d, want 200", code)
	}
	if len(done.DeletedTeams) != 1 || done.DeletedTeams[0] != mia.team {
		t.Fatalf("deleted teams = %v, want the member's own space %s", done.DeletedTeams, mia.team)
	}
	if code := f.call("GET", "/api/v1/me", mia.auth, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("the deleted account's session = %d, want 401", code)
	}
	var members []member
	f.call("GET", "/api/v1/team/members", owner.auth, nil, &members)
	if len(members) != 1 || members[0].UserID != owner.id {
		t.Fatalf("owner's team after the member's deletion = %+v", members)
	}
}

func TestOperatorDeletesAnAccountByEmailAndATeamBySlug(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	gone := f.user("g", "gus@example.com")
	f.createTeam(&owner, "acme")

	if code := f.call("DELETE", "/api/v1/accounts/gus@example.com", owner.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a team owner using the operator route = %d, want 403", code)
	}
	if code := f.call("DELETE", "/api/v1/accounts/nobody@example.com", "Bearer "+f.admin, nil, nil); code != http.StatusNotFound {
		t.Fatalf("an unknown address = %d, want 404", code)
	}
	if code := f.call("DELETE", "/api/v1/accounts/gus@example.com", "Bearer "+f.admin, nil, nil); code != http.StatusOK {
		t.Fatalf("operator deleting an account = %d, want 200", code)
	}
	if code := f.call("GET", "/api/v1/me", gone.auth, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("the deleted account's session = %d, want 401", code)
	}
	if code := f.call("DELETE", "/api/v1/teams/acme", owner.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a team owner using the operator team route = %d, want 403", code)
	}
	var del teamDeletionBody
	if code := f.call("DELETE", "/api/v1/teams/acme", "Bearer "+f.admin, nil, &del); code != http.StatusAccepted || del.State != store.TeamDeletionPending {
		t.Fatalf("operator deleting a team = %d %+v, want 202 pending", code, del)
	}
}

// mailFake records what the controller asked it to send.
type mailFake struct {
	mu   sync.Mutex
	sent []mailer.Message
}

func (m *mailFake) Send(_ context.Context, msg mailer.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mailFake) messages() []mailer.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mailer.Message(nil), m.sent...)
}

type inviteBody struct {
	ID        string `json:"id"`
	AcceptURL string `json:"accept_url"`
	EmailSent bool   `json:"email_sent"`
}

func TestInvitationEmailNamesTheInviterAndTeamAndStopsAtTheDailyCap(t *testing.T) {
	mail := &mailFake{}
	raw, pub := multiTeamLicense(t)
	f := newIdentityFixtureWith(t, fixtureOpts{license: raw, key: pub, configure: func(s *controller.Server) {
		s.WithMailer(mail)
	}})
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	var inv inviteBody
	if code := f.call("POST", "/api/v1/team/invitations", owner.auth,
		map[string]string{"email": "target@example.com", "role": "editor"}, &inv); code != http.StatusCreated {
		t.Fatalf("invite = %d", code)
	}
	if !inv.EmailSent || inv.AcceptURL == "" {
		t.Fatalf("invite response = %+v, want email_sent and an accept_url", inv)
	}
	sent := mail.messages()
	if len(sent) != 1 || sent[0].To != "target@example.com" {
		t.Fatalf("sent = %+v", sent)
	}
	for _, want := range []string{"olga Test", "acme", "editor", inv.AcceptURL} {
		if !strings.Contains(sent[0].Text, want) {
			t.Errorf("email text lacks %q:\n%s", want, sent[0].Text)
		}
	}
	if strings.Contains(sent[0].Text, owner.id) {
		t.Error("email names the inviter by account id")
	}

	for i := 1; i <= store.MaxInvitationEmailsPerDay; i++ {
		o := f.user(fmt.Sprintf("o%d", i), fmt.Sprintf("owner%d@example.com", i))
		var got inviteBody
		if code := f.call("POST", "/api/v1/team/invitations", o.auth,
			map[string]string{"email": "target@example.com", "role": "reader"}, &got); code != http.StatusCreated {
			t.Fatalf("invite %d = %d", i, code)
		}
		wantSent := i < store.MaxInvitationEmailsPerDay
		if got.EmailSent != wantSent || got.AcceptURL == "" {
			t.Fatalf("invite %d = %+v, want email_sent=%v and an accept_url", i, got, wantSent)
		}
	}
	if n := len(mail.messages()); n != store.MaxInvitationEmailsPerDay {
		t.Fatalf("sent %d emails to one address in a day, want %d", n, store.MaxInvitationEmailsPerDay)
	}
}

func TestInvitationWithoutAMailServiceSendsNothing(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	var inv inviteBody
	if code := f.call("POST", "/api/v1/team/invitations", owner.auth,
		map[string]string{"email": "target@example.com", "role": "reader"}, &inv); code != http.StatusCreated {
		t.Fatalf("invite = %d", code)
	}
	if inv.EmailSent || inv.AcceptURL == "" {
		t.Fatalf("invite response = %+v, want no email and an accept_url", inv)
	}
}
