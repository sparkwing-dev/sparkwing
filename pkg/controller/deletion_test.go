package controller_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type teamDeletionBody struct {
	Slug      string `json:"slug"`
	State     string `json:"state"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`
}

func newDeletionFixture(t *testing.T) *identityFixture {
	t.Helper()
	f := newIdentityFixtureWith(t, fixtureOpts{})
	return f
}

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
	if err := teststore.SeedTriggerWithRun(f.store, context.Background(), tn.Team(),
		store.Trigger{ID: id, Pipeline: "build", CreatedAt: now},
		store.Run{ID: id, Pipeline: "build", Status: "pending", StartedAt: now}); err != nil {
		f.t.Fatal(err)
	}
}

// bug: A cached tenant handle may still write until this window passes.
func afterCacheWindow() time.Time { return time.Now().Add(5 * time.Minute) }

func TestDeleteTeamClosesItAtOnceAndThePassRemovesItsRows(t *testing.T) {
	f := newDeletionFixture(t)
	owner := f.user("o", "olga@example.com")
	personal := owner.team
	f.createTeam(&owner, "acme")
	editor := f.user("e", "eddie@example.com")
	f.join(owner, editor, "eddie@example.com", "editor")
	var minted struct {
		Token string `json:"capability"`
	}
	if code := f.call("POST", "/api/v1/runners", owner.auth,
		map[string]any{"name": "box", "repos": []string{"github.com/acme/*"}}, &minted); code != http.StatusCreated || minted.Token == "" {
		t.Fatalf("mint runner token = %d", code)
	}
	if code := f.call("GET", "/api/v1/auth/whoami", "Bearer "+minted.Token, nil, nil); code != http.StatusOK {
		t.Fatalf("runner token before deletion = %d, want 200", code)
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
	if _, err := f.store.TeamInfo(context.Background(), "acme"); err != nil {
		t.Fatalf("a pass inside the tenant-cache window purged the team: %v", err)
	}
	f.srv.ProcessTeamDeletions(context.Background(), afterCacheWindow())
	var mine []teamDeletionBody
	f.call("GET", "/api/v1/me/team-deletions", owner.auth, nil, &mine)
	if len(mine) != 1 || mine[0].State != store.TeamDeletionDone {
		t.Fatalf("owner's deletions = %+v, want acme done", mine)
	}
	if _, err := f.store.TeamInfo(context.Background(), "acme"); err == nil {
		t.Fatal("the team is still registered after the pass")
	}
}

func TestDeleteTeamRefusesTheOnlyTeam(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": owner.team}, nil); code != http.StatusConflict {
		t.Fatalf("deleting the only team = %d, want 409", code)
	}
}

func TestOperatorDeletesAnAccountByEmailAndATeamBySlug(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	gone := f.user("g", "gus@example.com")
	f.createTeam(&owner, "acme")

	if code := f.call("DELETE", "/api/v1/operator/accounts/gus@example.com", owner.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a team owner using the operator route = %d, want 403", code)
	}
	if code := f.call("DELETE", "/api/v1/operator/accounts/nobody@example.com", f.consoleAuth(), nil, nil); code != http.StatusNotFound {
		t.Fatalf("an unknown address = %d, want 404", code)
	}
	if code := f.call("DELETE", "/api/v1/operator/accounts/gus@example.com", f.consoleAuth(), nil, nil); code != http.StatusOK {
		t.Fatalf("operator deleting an account = %d, want 200", code)
	}
	if code := f.call("GET", "/api/v1/me", gone.auth, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("the deleted account's session = %d, want 401", code)
	}
	if code := f.call("DELETE", "/api/v1/operator/teams/acme", owner.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a team owner using the operator team route = %d, want 403", code)
	}
	var del teamDeletionBody
	if code := f.call("DELETE", "/api/v1/operator/teams/acme", f.consoleAuth(), nil, &del); code != http.StatusAccepted || del.State != store.TeamDeletionPending {
		t.Fatalf("operator deleting a team = %d %+v, want 202 pending", code, del)
	}
}

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
	f := newIdentityFixtureWith(t, fixtureOpts{configure: func(s *controller.Server) {
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
