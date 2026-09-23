package controller_test

import (
	"net/http"
	"testing"
	"time"
)

type mintedRunner struct {
	Token  string `json:"token"`
	Prefix string `json:"prefix"`
}

func mintRunner(f *identityFixture, auth, name string) mintedRunner {
	f.t.Helper()
	var m mintedRunner
	if code := f.call("POST", "/api/v1/team/runner-tokens", auth, map[string]string{"name": name}, &m); code != http.StatusCreated {
		f.t.Fatalf("mint %s = %d", name, code)
	}
	return m
}

// runnerAuthenticates reports whether the controller still accepts a runner
// token: a live one reaches the team boundary and hears 404 for a missing run,
// a revoked one is refused before it.
func runnerAuthenticates(f *identityFixture, m mintedRunner) bool {
	f.t.Helper()
	code := f.call("GET", "/api/v1/triggers/no-such-run", "Bearer "+m.Token, nil, nil)
	switch code {
	case http.StatusNotFound:
		return true
	case http.StatusUnauthorized:
		return false
	}
	f.t.Fatalf("runner probe = %d", code)
	return false
}

// A runner token carries secrets.read and never expires on its own, so the
// machine a removed member connected stops being the team's in the same step.
func TestRemovingAMemberRevokesTheRunnerTokensTheyMinted(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, _ := teamOf(f)
	editorRunner := mintRunner(f, editor.auth, "eddie")
	ownerRunner := mintRunner(f, owner.auth, "olga")
	if !runnerAuthenticates(f, editorRunner) {
		t.Fatal("the editor's fresh runner token does not authenticate")
	}

	if code := f.call("DELETE", "/api/v1/team/members/"+editor.id, owner.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove = %d", code)
	}
	if runnerAuthenticates(f, editorRunner) {
		t.Error("a removed member's runner token still authenticates")
	}
	if !runnerAuthenticates(f, ownerRunner) {
		t.Error("removing the editor revoked the owner's runner token")
	}
}

// A reader may not mint a runner token, so a demotion to reader takes back
// the ones they already hold. A demotion that keeps the editor role does not.
func TestDemotingAMemberToReaderRevokesTheirRunnerTokens(t *testing.T) {
	f := newIdentityFixture(t)
	owner, editor, _ := teamOf(f)
	editorRunner := mintRunner(f, editor.auth, "eddie")
	ownerRunner := mintRunner(f, owner.auth, "olga")

	if code := f.call("PATCH", "/api/v1/team/members/"+editor.id, owner.auth, map[string]string{"role": "reader"}, nil); code != http.StatusNoContent {
		t.Fatalf("demote = %d", code)
	}
	if runnerAuthenticates(f, editorRunner) {
		t.Error("a member demoted to reader keeps a working runner token")
	}
	if !runnerAuthenticates(f, ownerRunner) {
		t.Error("demoting the editor revoked the owner's runner token")
	}
}

func TestRunnerTokensExpireAndTheListShowsWhen(t *testing.T) {
	f := newIdentityFixture(t)
	owner, _, _ := teamOf(f)
	before := time.Now()
	mintRunner(f, owner.auth, "olga")
	var listed []struct {
		Prefix    string `json:"prefix"`
		ExpiresAt *int64 `json:"expires_at"`
	}
	if code := f.call("GET", "/api/v1/team/runner-tokens", owner.auth, nil, &listed); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	if len(listed) != 1 || listed[0].ExpiresAt == nil {
		t.Fatalf("listed = %+v, want one token with an expiry", listed)
	}
	got := time.Unix(*listed[0].ExpiresAt, 0)
	want := before.Add(90 * 24 * time.Hour)
	if got.Before(want.Add(-time.Minute)) || got.After(want.Add(time.Minute)) {
		t.Errorf("expires_at = %s, want about %s", got, want)
	}
}
