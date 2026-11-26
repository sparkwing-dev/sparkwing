package controller_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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
		store.Run{RunSummary: runstate.RunSummary{ID: id, Pipeline: "build", Status: "pending", StartedAt: now}}); err != nil {
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
	var mine struct {
		Deletions  []teamDeletionBody `json:"deletions"`
		NextCursor string             `json:"next_cursor"`
		Truncated  bool               `json:"truncated"`
	}
	f.call("GET", "/api/v1/me/team-deletions", owner.auth, nil, &mine)
	if len(mine.Deletions) != 1 || mine.Deletions[0].State != store.TeamDeletionDone || mine.NextCursor != "" || mine.Truncated {
		t.Fatalf("owner's deletions = %+v, want acme done", mine)
	}
	if info, err := f.store.TeamInfo(t.Context(), "acme"); err != nil || info.DisplayName != "" || info.CreatedBy != "" {
		t.Fatalf("purged team tombstone = %+v: %v", info, err)
	}
	if _, err := f.store.ForTeam(t.Context(), "acme"); !errors.Is(err, store.ErrUnknownTeam) {
		t.Fatalf("purged team reopened: %v", err)
	}
	var runs int
	if err := teststore.SQL(t, f.store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM runs WHERE team='acme'`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("purged team runs = %d: %v", runs, err)
	}
}

func TestDeleteTeamRefusesTheOnlyTeam(t *testing.T) {
	for _, tc := range []struct {
		contact, want string
	}{
		{"", "this is your only team; delete your account instead"},
		{"support@example.test", "this is your only team; to delete it, ask support@example.test to delete your account"},
	} {
		f := newIdentityFixtureWith(t, fixtureOpts{configure: func(srv *controller.Server) {
			srv.WithDashboard(controller.Dashboard{SupportContact: tc.contact})
		}})
		owner := f.user("o", "olga@example.com")
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": owner.team}, &body); code != http.StatusConflict || body.Error.Message != tc.want {
			t.Fatalf("deleting the only team with contact %q = %d %q, want 409 %q", tc.contact, code, body.Error.Message, tc.want)
		}
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
	EmailNote string `json:"email_note"`
}

func TestInvitationEmailNamesTheInviterAndTeamAndStopsAtTheDailyCap(t *testing.T) {
	mail := &mailFake{}
	var logs controller.SyncBuffer
	f := newIdentityFixtureWith(t, fixtureOpts{
		logger:    slog.New(slog.NewTextHandler(&logs, nil)),
		configure: func(s *controller.Server) { s.WithMailer(mail) },
	})
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
	if out := logs.String(); !strings.Contains(out, "reason=recipient_limit") || strings.Contains(out, "target@example.com") {
		t.Fatalf("logs should name the recipient limit without the address:\n%s", out)
	}
}

func TestInvitationEmailPastTheAccountCapSaysWhenToRetry(t *testing.T) {
	mail := &mailFake{}
	var logs controller.SyncBuffer
	f := newIdentityFixtureWith(t, fixtureOpts{
		logger:    slog.New(slog.NewTextHandler(&logs, nil)),
		configure: func(s *controller.Server) { s.WithMailer(mail) },
	})
	owner := f.user("o", "olga@example.com")
	invite := func(email string) inviteBody {
		t.Helper()
		var got inviteBody
		if code := f.call("POST", "/api/v1/team/invitations", owner.auth,
			map[string]string{"email": email, "role": "reader"}, &got); code != http.StatusCreated {
			t.Fatalf("invite %s = %d", email, code)
		}
		return got
	}
	start := time.Now().UTC()
	for i := range store.MaxInvitationEmailsPerAccountPerDay {
		if i == 34 || i == 67 {
			f.createTeam(&owner, fmt.Sprintf("team-%d", i))
		}
		if got := invite(fmt.Sprintf("guest-%d@example.com", i)); !got.EmailSent || got.EmailNote != "" {
			t.Fatalf("invite %d = %+v, want an email and no note", i+1, got)
		}
	}
	got := invite("one-too-many@example.com")
	if got.EmailSent || got.AcceptURL == "" {
		t.Fatalf("invite past the account cap = %+v, want no email and an accept_url", got)
	}
	retry := start.Add(24 * time.Hour).Truncate(time.Minute).Add(time.Minute)
	for _, want := range []string{"reached its limit of 100 invitation emails in 24 hours", "again after"} {
		if !strings.Contains(got.EmailNote, want) {
			t.Fatalf("email note %q lacks %q", got.EmailNote, want)
		}
	}
	if !strings.Contains(got.EmailNote, retry.Format("15:04 UTC on Jan 2, 2006")) && !strings.Contains(got.EmailNote, retry.Add(time.Minute).Format("15:04 UTC on Jan 2, 2006")) {
		t.Fatalf("email note %q does not name the minute 24 hours after the first email (%s)", got.EmailNote, start)
	}
	for _, private := range []string{"team-34", "team-67", "guest-", owner.id} {
		if strings.Contains(got.EmailNote, private) {
			t.Fatalf("email note %q names %q", got.EmailNote, private)
		}
	}
	if n := len(mail.messages()); n != store.MaxInvitationEmailsPerAccountPerDay {
		t.Fatalf("sent %d invitation emails from one account in a day, want %d", n, store.MaxInvitationEmailsPerAccountPerDay)
	}
	if out := logs.String(); !strings.Contains(out, "reason=account_limit") || strings.Contains(out, "one-too-many@example.com") ||
		strings.Contains(out, "recipient_limit") {
		t.Fatalf("logs should name only the account limit, without the address:\n%s", out)
	}
}

// A display name or angle brackets around a real address would give one
// mailbox a fresh per-recipient daily allowance for every spelling.
func TestInvitationRefusesAddressFormsOtherThanAPlainAddress(t *testing.T) {
	mail := &mailFake{}
	f := newIdentityFixtureWith(t, fixtureOpts{configure: func(s *controller.Server) {
		s.WithMailer(mail)
	}})
	owner := f.user("o", "olga@example.com")
	for _, email := range []string{
		`"pay-invoice-at-evil.example"<target@example.com>`,
		`x<target@example.com>`,
		`<target@example.com>`,
		`target@example.com (x)`,
	} {
		var refusal struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		code := f.call("POST", "/api/v1/team/invitations", owner.auth,
			map[string]string{"email": email, "role": "reader"}, &refusal)
		if code != http.StatusBadRequest || !strings.Contains(refusal.Error.Message, "plain email address") {
			t.Errorf("invite %s = %d %q, want 400 asking for a plain email address", email, code, refusal.Error.Message)
		}
	}
	if sent := mail.messages(); len(sent) != 0 {
		t.Fatalf("sent %+v for refused addresses", sent)
	}
}

// A local part that needs quotes is still a plain address.
func TestInvitationAcceptsAQuotedLocalPart(t *testing.T) {
	mail := &mailFake{}
	f := newIdentityFixtureWith(t, fixtureOpts{configure: func(s *controller.Server) {
		s.WithMailer(mail)
	}})
	owner := f.user("o", "olga@example.com")
	var inv inviteBody
	if code := f.call("POST", "/api/v1/team/invitations", owner.auth,
		map[string]string{"email": `"qa..alerts"@example.com`, "role": "reader"}, &inv); code != http.StatusCreated {
		t.Fatalf("invite quoted local part = %d, want 201", code)
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
