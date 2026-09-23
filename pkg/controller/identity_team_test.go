package controller_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type member struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

type signedIn struct {
	auth string
	id   string
	team string
}

func (f *identityFixture) user(sub, email string) signedIn {
	f.t.Helper()
	out := f.signIn(person(sub, email, strings.Split(email, "@")[0]))
	return signedIn{auth: sessionAuth(out.SessionID), id: out.User.ID, team: out.ActiveTeam.Slug}
}

func (f *identityFixture) invite(owner signedIn, email, role string) string {
	f.t.Helper()
	var resp struct {
		ID        string `json:"id"`
		AcceptURL string `json:"accept_url"`
	}
	if code := f.call("POST", "/api/v1/team/invitations", owner.auth,
		map[string]string{"email": email, "role": role}, &resp); code != http.StatusCreated {
		f.t.Fatalf("invite %s = %d", email, code)
	}
	return resp.ID
}

func (f *identityFixture) join(owner, joiner signedIn, email, role string) {
	f.t.Helper()
	id := f.invite(owner, email, role)
	if code := f.call("POST", "/api/v1/invitations/"+id+"/accept", joiner.auth, nil, nil); code != http.StatusNoContent {
		f.t.Fatalf("accept = %d", code)
	}
}

func teamOf(f *identityFixture) (owner, editor, reader signedIn) {
	owner = f.user("o", "olga@example.com")
	editor = f.user("e", "eddie@example.com")
	reader = f.user("r", "rita@example.com")
	f.join(owner, editor, "eddie@example.com", "editor")
	f.join(owner, reader, "rita@example.com", "reader")
	editor.team, reader.team = owner.team, owner.team
	return owner, editor, reader
}

func TestInviteAcceptJoinsAndSwitchesTheTeam(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	x := f.user("x", "xavier@example.com")
	var resp struct {
		ID        string `json:"id"`
		AcceptURL string `json:"accept_url"`
	}
	f.call("POST", "/api/v1/team/invitations", owner.auth, map[string]string{"email": "Xavier@Example.com", "role": "editor"}, &resp)
	if resp.AcceptURL != "http://localhost:4343/invitations?id="+resp.ID {
		t.Fatalf("accept_url = %q", resp.AcceptURL)
	}
	var me meBody
	f.call("GET", "/api/v1/me", x.auth, nil, &me)
	if len(me.Invitations) != 1 || me.Invitations[0].ID != resp.ID || me.Invitations[0].TeamSlug != owner.team {
		t.Fatalf("invitee's /me invitations = %+v", me.Invitations)
	}
	var open []struct {
		ID, Email, Role string
		ExpiresAt       int64 `json:"expires_at"`
	}
	f.call("GET", "/api/v1/team/invitations", owner.auth, nil, &open)
	if len(open) != 1 || open[0].Email != "xavier@example.com" || open[0].ExpiresAt == 0 {
		t.Fatalf("open invitations = %+v", open)
	}
	if code := f.call("POST", "/api/v1/invitations/"+resp.ID+"/accept", x.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("accept = %d", code)
	}
	if w := f.whoami(x.auth); w.Team != owner.team || w.Role != "editor" {
		t.Fatalf("after accept whoami = %+v", w)
	}
	var members []member
	f.call("GET", "/api/v1/team/members", x.auth, nil, &members)
	if len(members) != 2 {
		t.Fatalf("members = %+v", members)
	}
}

func TestInvitationForOneEmailCannotBeAcceptedByAnother(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	f.user("x", "xavier@example.com")
	y := f.user("y", "yolanda@example.com")
	id := f.invite(owner, "xavier@example.com", "reader")
	if code := f.call("POST", "/api/v1/invitations/"+id+"/accept", y.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("y accepting x's invitation = %d, want 403", code)
	}
	if w := f.whoami(y.auth); w.Team == owner.team {
		t.Fatal("y got into the team")
	}
}

func TestUsedAndExpiredInvitationsAreRefused(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	x := f.user("x", "xavier@example.com")
	z := f.user("z", "zed@example.com")
	used := f.invite(owner, "xavier@example.com", "reader")
	if code := f.call("POST", "/api/v1/invitations/"+used+"/accept", x.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("first accept = %d", code)
	}
	if code := f.call("POST", "/api/v1/invitations/"+used+"/accept", x.auth, nil, nil); code != http.StatusGone {
		t.Fatalf("second accept = %d, want 410", code)
	}
	expired := f.invite(owner, "zed@example.com", "reader")
	if _, err := f.store.DB().Exec(`UPDATE invitations SET expires_at = 1 WHERE id = ?`, expired); err != nil {
		t.Fatal(err)
	}
	if code := f.call("POST", "/api/v1/invitations/"+expired+"/accept", z.auth, nil, nil); code != http.StatusGone {
		t.Fatalf("expired accept = %d, want 410", code)
	}
	if code := f.call("POST", "/api/v1/invitations/no-such-id/accept", z.auth, nil, nil); code != http.StatusNotFound {
		t.Fatalf("unknown invitation = %d, want 404", code)
	}
}

func TestReaderAndEditorCannotInvite(t *testing.T) {
	f := newIdentityFixture(t)
	_, editor, reader := teamOf(f)
	for name, u := range map[string]signedIn{"reader": reader, "editor": editor} {
		if code := f.call("POST", "/api/v1/team/invitations", u.auth,
			map[string]string{"email": "new@example.com", "role": "reader"}, nil); code != http.StatusForbidden {
			t.Fatalf("%s inviting = %d, want 403", name, code)
		}
		if code := f.call("GET", "/api/v1/team/invitations", u.auth, nil, nil); code != http.StatusForbidden {
			t.Fatalf("%s listing invitations = %d, want 403", name, code)
		}
	}
	var members []member
	if code := f.call("GET", "/api/v1/team/members", reader.auth, nil, &members); code != http.StatusOK || len(members) != 3 {
		t.Fatalf("reader listing members = %d, %d rows", code, len(members))
	}
}

func TestEditorCannotChangeRoles(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, reader := teamOf(f)
	if code := f.call("PATCH", "/api/v1/team/members/"+reader.id, editor.auth, map[string]string{"role": "editor"}, nil); code != http.StatusForbidden {
		t.Fatalf("editor promoting a reader = %d, want 403", code)
	}
	if code := f.call("PATCH", "/api/v1/team/members/"+editor.id, editor.auth, map[string]string{"role": "owner"}, nil); code != http.StatusForbidden {
		t.Fatalf("editor promoting self = %d, want 403", code)
	}
	if code := f.call("DELETE", "/api/v1/team/members/"+reader.id, editor.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("editor removing a reader = %d, want 403", code)
	}
	if code := f.call("PATCH", "/api/v1/team", editor.auth, map[string]string{"display_name": "Mine"}, nil); code != http.StatusForbidden {
		t.Fatalf("editor renaming the team = %d, want 403", code)
	}
	if code := f.call("PATCH", "/api/v1/team/members/"+reader.id, owner.auth, map[string]string{"role": "editor"}, nil); code != http.StatusNoContent {
		t.Fatalf("owner promoting a reader = %d", code)
	}
	if code := f.call("PATCH", "/api/v1/team", owner.auth, map[string]string{"display_name": "Olga's crew"}, nil); code != http.StatusOK {
		t.Fatalf("owner renaming = %d", code)
	}
}

func TestLastOwnerCannotLeaveOrBeDemoted(t *testing.T) {
	f := newIdentityFixture(t)
	owner, _, reader := teamOf(f)
	if code := f.call("DELETE", "/api/v1/team/members/"+owner.id, owner.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("last owner leaving = %d, want 403", code)
	}
	if code := f.call("PATCH", "/api/v1/team/members/"+owner.id, owner.auth, map[string]string{"role": "editor"}, nil); code != http.StatusForbidden {
		t.Fatalf("last owner demoting self = %d, want 403", code)
	}
	if code := f.call("DELETE", "/api/v1/team/members/"+reader.id, reader.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("reader leaving = %d, want 204", code)
	}
	if code := f.call("GET", "/api/v1/team/members", reader.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a reader who left still lists members: %d", code)
	}
}

func TestDemotionTakesEffectOnTheNextRequest(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, _ := teamOf(f)
	if code := f.call("POST", "/api/v1/team/runner-tokens", editor.auth, map[string]string{"name": "laptop"}, nil); code != http.StatusCreated {
		t.Fatalf("editor minting = %d", code)
	}
	f.call("PATCH", "/api/v1/team/members/"+editor.id, owner.auth, map[string]string{"role": "reader"}, nil)
	if code := f.call("POST", "/api/v1/team/runner-tokens", editor.auth, map[string]string{"name": "laptop2"}, nil); code != http.StatusForbidden {
		t.Fatalf("demoted editor minting = %d, want 403", code)
	}
}

// Every /team route acts on the caller's own active team. A team slug placed
// in the query, or an id that belongs to another team, reaches nothing there.
func TestTeamRoutesNeverReachAnotherTeam(t *testing.T) {
	f := newIdentityFixture(t)
	a := f.user("a", "alpha@example.com")
	b := f.user("b", "bravo@example.com")
	bReader := f.user("br", "bread@example.com")
	f.join(b, bReader, "bread@example.com", "reader")
	bInvite := f.invite(b, "pending@example.com", "reader")
	var minted struct{ Prefix string }
	f.call("POST", "/api/v1/team/runner-tokens", b.auth, map[string]string{"name": "b-box"}, &minted)

	q := "?team=" + b.team
	if code := f.call("DELETE", "/api/v1/team/members/"+bReader.id+q, a.auth, nil, nil); code != http.StatusNotFound {
		t.Fatalf("a removing b's member = %d, want 404", code)
	}
	if code := f.call("PATCH", "/api/v1/team/members/"+bReader.id+q, a.auth, map[string]string{"role": "owner"}, nil); code != http.StatusNotFound {
		t.Fatalf("a re-roling b's member = %d, want 404", code)
	}
	if code := f.call("DELETE", "/api/v1/team/invitations/"+bInvite+q, a.auth, nil, nil); code != http.StatusNotFound {
		t.Fatalf("a deleting b's invitation = %d, want 404", code)
	}
	if code := f.call("DELETE", "/api/v1/team/runner-tokens/"+minted.Prefix+q, a.auth, nil, nil); code != http.StatusNotFound {
		t.Fatalf("a revoking b's runner token = %d, want 404", code)
	}
	var members []member
	f.call("GET", "/api/v1/team/members"+q, a.auth, nil, &members)
	if len(members) != 1 || members[0].UserID != a.id {
		t.Fatalf("a's member list reaches another team: %+v", members)
	}
	var toks []struct{ Prefix string }
	f.call("GET", "/api/v1/team/runner-tokens"+q, a.auth, nil, &toks)
	if len(toks) != 0 {
		t.Fatalf("a lists b's runner tokens: %+v", toks)
	}
	var open []struct{ ID string }
	f.call("GET", "/api/v1/team/invitations"+q, a.auth, nil, &open)
	if len(open) != 0 {
		t.Fatalf("a lists b's invitations: %+v", open)
	}
}

func TestRunnerTokenIsBoundToTheTeamThatMintedIt(t *testing.T) {
	f := newIdentityFixture(t)
	a := f.user("a", "alpha@example.com")
	b := f.user("b", "bravo@example.com")
	var minted struct {
		Token   string `json:"token"`
		Prefix  string `json:"prefix"`
		Command string `json:"command"`
	}
	if code := f.call("POST", "/api/v1/team/runner-tokens", a.auth, map[string]string{"name": "a-box"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint = %d", code)
	}
	if !strings.HasPrefix(minted.Command, "SPARKWING_AGENT_TOKEN="+minted.Token+" sparkwing-runner runner --controller ") ||
		!strings.Contains(minted.Command, " --logs ") ||
		!strings.Contains(minted.Command, " --also-claim-triggers ") ||
		!strings.Contains(minted.Command, " --max-claims-before-restart 0 ") ||
		strings.Contains(minted.Command, "--gitcache") ||
		!strings.HasSuffix(minted.Command, " --holder-prefix a-box") {
		t.Fatalf("command = %q", minted.Command)
	}
	runner := "Bearer " + minted.Token
	w := f.whoami(runner)
	if w.Team != a.team || w.Principal != "agent:a-box" {
		t.Fatalf("runner whoami = %+v", w)
	}
	for _, s := range []string{controller.ScopeAdmin, controller.ScopeTeamAdmin, controller.ScopeRunsWrite} {
		for _, have := range w.Scopes {
			if have == s {
				t.Fatalf("runner token carries %s", s)
			}
		}
	}
	if code := f.call("GET", "/api/v1/team/runner-tokens", runner, nil, nil); code != http.StatusForbidden && code != http.StatusUnauthorized {
		t.Fatalf("runner token listing team tokens = %d", code)
	}
	if code := f.call("DELETE", "/api/v1/team/runner-tokens/"+minted.Prefix, b.auth, nil, nil); code != http.StatusNotFound {
		t.Fatalf("team b revoking team a's token = %d, want 404", code)
	}
	if code := f.call("GET", "/api/v1/auth/whoami", runner, nil, nil); code != http.StatusOK {
		t.Fatalf("token stopped working after another team's revoke attempt: %d", code)
	}
}

func TestEditorRevokesOnlyTheirOwnRunnerTokens(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, _ := teamOf(f)
	other := f.user("e2", "ernie@example.com")
	f.join(owner, other, "ernie@example.com", "editor")
	var mine struct{ Token, Prefix string }
	f.call("POST", "/api/v1/team/runner-tokens", editor.auth, map[string]string{"name": "eddie-box"}, &mine)
	if code := f.call("DELETE", "/api/v1/team/runner-tokens/"+mine.Prefix, other.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("another editor revoking = %d, want 403", code)
	}
	var list []struct {
		Prefix    string `json:"prefix"`
		Name      string `json:"name"`
		CreatedBy string `json:"created_by"`
	}
	f.call("GET", "/api/v1/team/runner-tokens", owner.auth, nil, &list)
	if len(list) != 1 || list[0].Name != "eddie-box" || list[0].CreatedBy != editor.id {
		t.Fatalf("list = %+v", list)
	}
	if code := f.call("DELETE", "/api/v1/team/runner-tokens/"+mine.Prefix, owner.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("owner revoking = %d", code)
	}
	if code := f.call("GET", "/api/v1/auth/whoami", "Bearer "+mine.Token, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked token still authenticates: %d", code)
	}
}

func TestReaderCannotMintRunnerTokens(t *testing.T) {
	f := newIdentityFixture(t)
	_, _, reader := teamOf(f)
	if code := f.call("POST", "/api/v1/team/runner-tokens", reader.auth, map[string]string{"name": "r"}, nil); code != http.StatusForbidden {
		t.Fatalf("reader minting = %d, want 403", code)
	}
	owner := f.user("o2", "owen@example.com")
	if code := f.call("POST", "/api/v1/team/runner-tokens", owner.auth, map[string]string{"name": "x; rm -rf /"}, nil); code != http.StatusBadRequest {
		t.Fatalf("shell-unsafe runner name = %d, want 400", code)
	}
}

func TestRunnerTokensAreCappedPerTeam(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	for i := range store.MaxRunnerTokensPerTeam {
		if code := f.call("POST", "/api/v1/team/runner-tokens", owner.auth, map[string]string{"name": fmt.Sprintf("box%d", i)}, nil); code != http.StatusCreated {
			t.Fatalf("mint %d = %d", i, code)
		}
	}
	if code := f.call("POST", "/api/v1/team/runner-tokens", owner.auth, map[string]string{"name": "box-extra"}, nil); code != http.StatusConflict {
		t.Fatalf("mint past the cap = %d, want 409", code)
	}
}
