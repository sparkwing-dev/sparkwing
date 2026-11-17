package controller_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func TestSelfDeletePreviewExplainsStaleSignInAndArchive(t *testing.T) {
	f := newDeletionFixture(t)
	u := f.user("reauth", "reauth@example.test")
	var plan struct {
		SignInRequired bool   `json:"sign_in_required"`
		ArchiveNotice  string `json:"archive_notice"`
	}
	if code := f.call("GET", "/api/v1/me/deletion", u.auth, nil, &plan); code != http.StatusOK || plan.SignInRequired {
		t.Fatalf("fresh preview=%d %+v", code, plan)
	}
	var expected string
	for _, entry := range docs.List() {
		if entry.Slug == "account-deletion-retention" {
			expected = entry.Summary
		}
	}
	if expected == "" || plan.ArchiveNotice != expected {
		t.Fatalf("preview omitted documentation-owned archive notice: %+v", plan)
	}
	f.advance(11 * time.Minute)
	if code := f.call("DELETE", "/api/v1/me", u.auth, map[string]any{"confirm_account_id": u.id, "confirm_teams": []string{u.team}}, nil); code != http.StatusForbidden {
		t.Fatalf("stale deletion=%d", code)
	}
	if code := f.call("GET", "/api/v1/me/deletion", u.auth, nil, &plan); code != http.StatusOK || !plan.SignInRequired {
		t.Fatalf("stale preview=%d %+v", code, plan)
	}
	if code := f.call("GET", "/api/v1/me", u.auth, nil, nil); code != http.StatusOK {
		t.Fatalf("stale refusal removed account: %d", code)
	}
}

func TestSelfDeleteConfirmsEverySoleMemberTeamAndRevokesAccount(t *testing.T) {
	f := newDeletionFixture(t)
	u := f.user("self", "self@example.test")
	personal := u.team
	f.createTeam(&u, "own-team")
	f.seedRun("own-team", "gone-run")
	var runner struct {
		Token string `json:"capability"`
	}
	if code := f.call("POST", "/api/v1/runners", u.auth, map[string]any{"name": "owned-runner", "repos": []string{"github.com/test/*"}}, &runner); code != http.StatusCreated {
		t.Fatalf("runner token = %d", code)
	}
	var plan struct {
		AccountID string   `json:"account_id"`
		Teams     []string `json:"teams"`
	}
	if code := f.call("GET", "/api/v1/me/deletion", u.auth, nil, &plan); code != http.StatusOK {
		t.Fatalf("preview = %d, want 200", code)
	}
	if plan.AccountID != u.id || len(plan.Teams) != 2 {
		t.Fatalf("preview = %+v", plan)
	}
	for _, body := range []any{
		map[string]any{},
		map[string]any{"confirm_account_id": "someone-else", "confirm_teams": []string{personal, "own-team"}},
		map[string]any{"confirm_account_id": u.id, "confirm_teams": []string{personal}},
	} {
		if code := f.call("DELETE", "/api/v1/me", u.auth, body, nil); code != http.StatusBadRequest && code != http.StatusConflict {
			t.Fatalf("bad confirmation = %d", code)
		}
		if code := f.call("GET", "/api/v1/me", u.auth, nil, nil); code != http.StatusOK {
			t.Fatalf("refusal revoked account = %d", code)
		}
	}
	if code := f.call("DELETE", "/api/v1/me", u.auth, map[string]any{"confirm_account_id": u.id, "confirm_teams": []string{personal, "own-team"}}, nil); code != http.StatusOK {
		t.Fatalf("delete = %d, want 200", code)
	}
	if code := f.call("GET", "/api/v1/me", u.auth, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("deleted session = %d", code)
	}
	if code := f.call("GET", "/api/v1/auth/whoami", "Bearer "+runner.Token, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("owned runner token survived: %d", code)
	}
	var audits int
	if err := teststore.SQL(t, f.store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_events WHERE kind='account.deleted' AND subject_id=?`, u.id).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit = %d, %v", audits, err)
	}
	f.srv.ProcessTeamDeletions(t.Context(), time.Now().Add(5*time.Minute))
	var runs int
	if err := teststore.SQL(t, f.store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM runs WHERE team='own-team'`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("purged runs = %d, %v", runs, err)
	}
}

func TestSelfDeleteWaitlistedAccountWithoutATeam(t *testing.T) {
	f := newIdentityFixture(t)
	f.setSignUp(map[string]any{"mode": "waitlist"})
	u := f.signIn(person("waiting-deletion", "waiting@example.test", "Waiting"))
	auth := sessionAuth(u.SessionID)
	if code := f.call("GET", "/api/v1/me/deletion", auth, nil, nil); code != http.StatusOK {
		t.Fatalf("waitlist preview=%d", code)
	}
	if code := f.call("DELETE", "/api/v1/me", auth, map[string]any{"confirm_account_id": u.User.ID, "confirm_teams": []string{}}, nil); code != http.StatusOK {
		t.Fatalf("waitlist delete=%d", code)
	}
	if code := f.call("GET", "/api/v1/me", auth, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("waitlist session survived=%d", code)
	}
}

func TestSelfDeleteRefusesLastOwnerAndKeepsSharedHistory(t *testing.T) {
	f := newDeletionFixture(t)
	owner := f.user("owner", "owner@example.test")
	f.createTeam(&owner, "shared-team")
	member := f.user("member", "member@example.test")
	f.join(owner, member, "member@example.test", "editor")
	if code := f.call("DELETE", "/api/v1/me", owner.auth, map[string]any{"confirm_account_id": owner.id, "confirm_teams": []string{}}, nil); code != http.StatusConflict {
		t.Fatalf("last owner = %d", code)
	}
	f.seedRun("shared-team", "shared-run")
	if code := f.call("DELETE", "/api/v1/me", member.auth, map[string]any{"confirm_account_id": member.id, "confirm_teams": []string{member.team}}, nil); code != http.StatusOK {
		t.Fatalf("member deletion = %d", code)
	}
	team, err := f.store.ForTeam(t.Context(), store.Team("shared-team"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := team.GetRun(t.Context(), "shared-run"); err != nil {
		t.Fatalf("shared history lost: %v", err)
	}
}
