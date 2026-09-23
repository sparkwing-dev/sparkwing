package controller_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

type mintedCLI struct {
	Token     string   `json:"token"`
	Prefix    string   `json:"prefix"`
	Scopes    []string `json:"scopes"`
	ExpiresAt int64    `json:"expires_at"`
	Profile   string   `json:"profile"`
	Setup     string   `json:"setup"`
	Run       string   `json:"run"`
}

func mintCLI(f *identityFixture, who signedIn) mintedCLI {
	f.t.Helper()
	var m mintedCLI
	if code := f.call("POST", "/api/v1/team/cli-tokens", who.auth, nil, &m); code != http.StatusCreated {
		f.t.Fatalf("mint CLI token = %d", code)
	}
	return m
}

func cliTrigger(f *identityFixture, token string) (int, string) {
	f.t.Helper()
	var out struct {
		RunID string `json:"run_id"`
	}
	code := f.call("POST", "/api/v1/triggers", "Bearer "+token, map[string]any{
		"pipeline": "build",
		"trigger":  map[string]any{"source": "pipeline-trigger@laptop"},
		"git": map[string]any{
			"branch": "main", "sha": strings.Repeat("a", 40),
			"repo_url": "https://github.com/acme/app.git",
		},
	}, &out)
	return code, out.RunID
}

// cliAuthenticates reports whether the controller still accepts a CLI token:
// a live one reaches the team boundary and hears 404 for a missing run, a
// revoked one is refused before it.
func cliAuthenticates(f *identityFixture, m mintedCLI) bool {
	f.t.Helper()
	switch code := f.call("GET", "/api/v1/runs/no-such-run", "Bearer "+m.Token, nil, nil); code {
	case http.StatusNotFound:
		return true
	case http.StatusUnauthorized:
		return false
	default:
		f.t.Fatalf("CLI probe = %d", code)
		return false
	}
}

func TestEditorCLITokenStartsARunTheTeamSees(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, _ := teamOf(f)
	before := time.Now()
	m := mintCLI(f, editor)

	if slices.Contains(m.Scopes, "team.admin") || !slices.Contains(m.Scopes, "runs.write") {
		t.Fatalf("scopes = %v, want the editor's without team.admin", m.Scopes)
	}
	if !strings.HasPrefix(m.Token, "swu_") {
		t.Fatalf("token %q is not a user token", m.Token)
	}
	if got, want := time.Unix(m.ExpiresAt, 0), before.Add(90*24*time.Hour); got.Before(want.Add(-time.Minute)) || got.After(want.Add(time.Minute)) {
		t.Errorf("expires_at = %s, want about %s", got, want)
	}
	if m.Profile != editor.team {
		t.Errorf("profile = %q, want the team slug %q", m.Profile, editor.team)
	}
	for _, want := range []string{"sparkwing cloud connect --controller http", " --name " + editor.team, " --token-stdin"} {
		if !strings.Contains(m.Setup, want) {
			t.Errorf("setup %q lacks %q", m.Setup, want)
		}
	}
	if strings.Contains(m.Setup, m.Token) {
		t.Error("the setup command carries the token, which would land in shell history")
	}
	if m.Run != "sparkwing pipeline trigger <pipeline> --profile "+editor.team {
		t.Errorf("run = %q", m.Run)
	}

	code, runID := cliTrigger(f, m.Token)
	if code != http.StatusAccepted {
		t.Fatalf("trigger with the CLI token = %d", code)
	}
	var trig struct {
		RepoURL string `json:"repo_url"`
		GitSHA  string `json:"git_sha"`
	}
	if code := f.call("GET", "/api/v1/triggers/"+runID, owner.auth, nil, &trig); code != http.StatusOK {
		t.Fatalf("the owner reads the editor's trigger = %d", code)
	}
	if trig.RepoURL != "https://github.com/acme/app.git" || trig.GitSHA != strings.Repeat("a", 40) {
		t.Errorf("trigger = %+v, want the repository and commit the CLI sent", trig)
	}
}

// A terminal credential never administers the team, and never mints another
// credential, whatever the role of the member it acts for.
func TestCLITokenCannotAdministerOrMint(t *testing.T) {
	f := newIdentityFixture(t)
	owner, _, _ := teamOf(f)
	m := mintCLI(f, owner)
	if slices.Contains(m.Scopes, "team.admin") {
		t.Fatalf("an owner's CLI token carries team.admin: %v", m.Scopes)
	}
	bearer := "Bearer " + m.Token
	if code := f.call("PATCH", "/api/v1/team", bearer, map[string]string{"display_name": "x"}, nil); code != http.StatusForbidden {
		t.Errorf("rename team with a CLI token = %d, want 403", code)
	}
	if code := f.call("POST", "/api/v1/team/cli-tokens", bearer, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("mint a CLI token with a CLI token = %d, want 401", code)
	}
	if code := f.call("POST", "/api/v1/team/runner-tokens", bearer, map[string]any{"name": "x", "repos": []string{"github.com/acme/*"}}, nil); code != http.StatusUnauthorized {
		t.Errorf("mint a runner token with a CLI token = %d, want 401", code)
	}
}

func TestReaderCLITokenOnlyReads(t *testing.T) {
	f := newIdentityFixture(t)
	_, _, reader := teamOf(f)
	m := mintCLI(f, reader)
	if slices.Contains(m.Scopes, "runs.write") || !slices.Contains(m.Scopes, "runs.read") {
		t.Fatalf("scopes = %v, want read-only", m.Scopes)
	}
	if code, _ := cliTrigger(f, m.Token); code != http.StatusForbidden {
		t.Errorf("trigger with a reader's CLI token = %d, want 403", code)
	}
	if code := f.call("GET", "/api/v1/runs", "Bearer "+m.Token, nil, nil); code != http.StatusOK {
		t.Errorf("list runs with a reader's CLI token = %d, want 200", code)
	}
}

func TestCLITokenGoesWithTheMembersAccess(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, reader := teamOf(f)
	editorCLI := mintCLI(f, editor)
	readerCLI := mintCLI(f, reader)
	ownerCLI := mintCLI(f, owner)

	if code := f.call("PATCH", "/api/v1/team/members/"+editor.id, owner.auth, map[string]string{"role": "reader"}, nil); code != http.StatusNoContent {
		t.Fatalf("demote = %d", code)
	}
	if cliAuthenticates(f, editorCLI) {
		t.Error("a member demoted to reader keeps a CLI token that writes")
	}
	if code := f.call("DELETE", "/api/v1/team/members/"+reader.id, owner.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove = %d", code)
	}
	if cliAuthenticates(f, readerCLI) {
		t.Error("a removed member's CLI token still authenticates")
	}
	if !cliAuthenticates(f, ownerCLI) {
		t.Error("changing other members revoked the owner's CLI token")
	}
}

// A CLI token is personal: its member lists and revokes it, and nobody else
// sees or revokes it.
func TestCLITokensArePersonal(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, _ := teamOf(f)
	mine := mintCLI(f, editor)
	mintCLI(f, owner)

	var listed []struct {
		Prefix    string `json:"prefix"`
		ExpiresAt *int64 `json:"expires_at"`
	}
	if code := f.call("GET", "/api/v1/team/cli-tokens", editor.auth, nil, &listed); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	if len(listed) != 1 || listed[0].Prefix != mine.Prefix || listed[0].ExpiresAt == nil {
		t.Fatalf("listed = %+v, want only the editor's token, with its expiry", listed)
	}
	if code := f.call("DELETE", "/api/v1/team/cli-tokens/"+mine.Prefix, owner.auth, nil, nil); code != http.StatusNotFound {
		t.Errorf("the owner revoking the editor's CLI token = %d, want 404", code)
	}
	if !cliAuthenticates(f, mine) {
		t.Fatal("another member's revoke reached the token")
	}
	if code := f.call("DELETE", "/api/v1/team/cli-tokens/"+mine.Prefix, editor.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke = %d", code)
	}
	if cliAuthenticates(f, mine) {
		t.Error("a revoked CLI token still authenticates")
	}
}
