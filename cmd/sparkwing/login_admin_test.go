package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func (f *deviceLoginFixture) status(t *testing.T, bearer, method, path string) int {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w.Code
}

func (f *deviceLoginFixture) revoked(t *testing.T, token string) bool {
	t.Helper()
	prefix, err := tokenPrefix(token)
	if err != nil {
		t.Fatal(err)
	}
	var revoked bool
	if err := teststore.SQL(t, f.state).QueryRowContext(t.Context(), `SELECT revoked_at IS NOT NULL FROM capabilities WHERE prefix=?`, prefix).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	return revoked
}

// hack: grants is a BLOB in SQLite, so a LIKE on the raw column never matches; cast it to text.
func (f *deviceLoginFixture) liveAdmins(t *testing.T) int {
	t.Helper()
	var n int
	if err := teststore.SQL(t, f.state).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities WHERE CAST(grants AS TEXT) LIKE '%team.admin%' AND revoked_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLoginAdminSavesAnHourOfAdministrationBesideTheMemberToken(t *testing.T) {
	writeProfilesFixture(t, "profiles: {}\n")
	f := newDeviceLoginFixtureAs(t, "approve", "owner")
	captureStdout(t, func() {
		if err := f.run(t, "login", "--controller", "https://controller.example", "--profile", "work", "--machine", "desk", "-o", "json"); err != nil {
			t.Fatal(err)
		}
	})
	profiles, _, err := loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	first := *profiles["work"].Controller.Token
	if code := f.status(t, first, http.MethodGet, "/api/v1/subscriptions"); code != http.StatusForbidden {
		t.Fatalf("member token reached an admin route: %d", code)
	}

	before := time.Now()
	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() {
			if err := f.run(t, "login", "--admin", "--profile", "work", "--machine", "desk", "-o", "json"); err != nil {
				t.Fatal(err)
			}
		})
	})
	profiles, _, err = loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	saved := &namedProfile{Name: "work", Profile: profiles["work"]}
	c := saved.Controller
	if c.Token == nil || *c.Token == first || c.AdminToken == nil || c.AdminExpiresAt == nil {
		t.Fatalf("saved profile = %+v", c)
	}
	if left := c.AdminExpiresAt.Sub(before); left < 59*time.Minute || left > 61*time.Minute {
		t.Fatalf("administration lasts %v, want an hour", left)
	}
	if saved.ControllerToken() != *c.AdminToken {
		t.Fatal("a live administration token is not the profile's bearer")
	}
	if code := f.status(t, saved.ControllerToken(), http.MethodGet, "/api/v1/subscriptions"); code != http.StatusOK {
		t.Fatalf("administration token on an admin route: %d", code)
	}
	if code := f.status(t, *c.Token, http.MethodGet, "/api/v1/subscriptions"); code != http.StatusForbidden {
		t.Fatalf("re-issued member token on an admin route: %d", code)
	}
	if !f.revoked(t, first) {
		t.Fatal("the replaced member token is still live")
	}
	if live := f.liveAdmins(t); live != 1 {
		t.Fatalf("live team.admin credentials = %d, want the one just issued", live)
	}
	if strings.Contains(out, *c.AdminToken) || strings.Contains(out, *c.Token) || !strings.Contains(out, `"admin_expires_at"`) {
		t.Fatalf("login --admin output = %s", out)
	}
	if !strings.Contains(stderr, "team.admin") || !strings.Contains(stderr, "until") {
		t.Fatalf("login --admin stderr = %s", stderr)
	}
	var record profileIndex
	if err := json.Unmarshal([]byte(out), &record); err != nil || record.AdminExpiresAt == nil {
		t.Fatalf("profile record %s %v", out, err)
	}

	expired := *saved
	past := time.Now().Add(-time.Minute)
	expired.Controller.AdminExpiresAt = &past
	if expired.ControllerToken() != *c.Token {
		t.Fatal("an expired administration token is still the profile's bearer")
	}
	if expiredRecord := profileRecord(&expired); expiredRecord.AdminExpiresAt != nil {
		t.Fatalf("an expired administration token is still reported: %+v", expiredRecord)
	}

	inv, err := clisurface.Parse("sparkwing", []string{"profiles", "remove", "--profile", "work", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		if err := removeProfile(inv, "json"); err != nil {
			t.Fatal(err)
		}
	})
	if !f.revoked(t, *c.Token) || !f.revoked(t, *c.AdminToken) {
		t.Fatal("profiles remove left the member or administration token live")
	}
}

func TestLoginAdminNeedsASignedInProfile(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  work:\n    controller: { url: https://controller.example }\n")
	f := newDeviceLoginFixtureAs(t, "approve", "owner")
	for _, args := range [][]string{
		{"login", "--admin", "--profile", "work"},
		{"login", "--admin", "--profile", "missing"},
		{"login", "--admin", "--profile", "work", "--scope", "runs.read"},
	} {
		err := f.run(t, args...)
		var usage *clisurface.UsageError
		if !errors.As(err, &usage) {
			t.Fatalf("%v: %v, want a usage error", args, err)
		}
	}
}

func TestLoginAdminSavesNothingForAnEditor(t *testing.T) {
	writeProfilesFixture(t, "profiles: {}\n")
	f := newDeviceLoginFixtureAs(t, "approve", "editor")
	captureStdout(t, func() {
		if err := f.run(t, "login", "--controller", "https://controller.example", "--profile", "work", "-o", "json"); err != nil {
			t.Fatal(err)
		}
	})
	profiles, _, err := loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	member := *profiles["work"].Controller.Token
	err = f.run(t, "login", "--admin", "--profile", "work", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("editor step-up: %v", err)
	}
	if len(f.refusals) != 1 || f.refusals[0] != http.StatusForbidden {
		t.Fatalf("editor approval refusals = %v, want one 403", f.refusals)
	}
	profiles, _, err = loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if c := profiles["work"].Controller; c.AdminToken != nil || c.AdminExpiresAt != nil || *c.Token != member {
		t.Fatalf("editor step-up changed the profile: %+v", c)
	}
	if admins := f.liveAdmins(t); admins != 0 {
		t.Fatalf("%d team.admin credentials after an editor step-up", admins)
	}
}

func TestLoginRevokesTheAdminTokenEvenWhenTheMemberTokenAlreadyEnded(t *testing.T) {
	writeProfilesFixture(t, "profiles: {}\n")
	f := newDeviceLoginFixtureAs(t, "approve", "owner")
	captureStdout(t, func() {
		if err := f.run(t, "login", "--controller", "https://controller.example", "--profile", "work"); err != nil {
			t.Fatal(err)
		}
	})
	captureStdout(t, func() {
		if err := f.run(t, "login", "--admin", "--profile", "work"); err != nil {
			t.Fatal(err)
		}
	})
	profiles, _, err := loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	previous := profiles["work"].Controller
	if err := revokeToken(previous.URL, *previous.Token); err != nil {
		t.Fatal(err)
	}
	if code := f.status(t, *previous.AdminToken, http.MethodGet, "/api/v1/subscriptions"); code != http.StatusOK {
		t.Fatalf("precondition: administration token answered %d", code)
	}
	captureStdout(t, func() {
		if err := f.run(t, "login", "--profile", "work"); err != nil {
			t.Fatal(err)
		}
	})
	if code := f.status(t, *previous.AdminToken, http.MethodGet, "/api/v1/subscriptions"); code != http.StatusUnauthorized {
		t.Fatalf("the replaced administration token answered %d after a normal login, want 401", code)
	}
}

func TestLoginAdminRevokesWhatAFailedSaveLeftUnstored(t *testing.T) {
	writeProfilesFixture(t, "profiles: {}\n")
	f := newDeviceLoginFixtureAs(t, "approve", "owner")
	captureStdout(t, func() {
		if err := f.run(t, "login", "--controller", "https://controller.example", "--profile", "work"); err != nil {
			t.Fatal(err)
		}
	})
	original, changed := f.console.sleep, false
	f.console.sleep = func(ctx context.Context, d time.Duration) error {
		if err := original(ctx, d); err != nil || changed {
			return err
		}
		changed = true
		profiles, path, err := loadProfiles()
		if err != nil {
			return err
		}
		return userconfig.Update(path, userconfig.Profiles, "profiles", &profiles, func(bool) error {
			p := profiles["work"]
			token := "changed-in-another-terminal"
			p.Controller.Token = &token
			profiles["work"] = p
			return nil
		})
	}
	if err := f.run(t, "login", "--admin", "--profile", "work"); err == nil || !strings.Contains(err.Error(), "changed while waiting") {
		t.Fatalf("login --admin over a changed profile: %v", err)
	}
	if live := f.liveAdmins(t); live != 0 {
		t.Fatalf("a failed save left %d live administration token(s)", live)
	}
	var liveMembers int
	if err := teststore.SQL(t, f.state).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities WHERE subject_id='member' AND revoked_at IS NULL`).Scan(&liveMembers); err != nil || liveMembers != 1 {
		t.Fatalf("live member credentials = %d %v, want only the first login's token", liveMembers, err)
	}
}

func TestLoginFailsClosedWhenTheOldAdminTokenCannotBeRevoked(t *testing.T) {
	writeProfilesFixture(t, "profiles: {}\n")
	f := newDeviceLoginFixtureAs(t, "approve", "owner")
	captureStdout(t, func() {
		if err := f.run(t, "login", "--controller", "https://controller.example", "--profile", "work"); err != nil {
			t.Fatal(err)
		}
	})
	captureStdout(t, func() {
		if err := f.run(t, "login", "--admin", "--profile", "work"); err != nil {
			t.Fatal(err)
		}
	})
	profiles, _, err := loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	old := profiles["work"].Controller
	if err := revokeToken(old.URL, *old.Token); err != nil {
		t.Fatal(err)
	}
	transport, attempts := http.DefaultTransport, 0
	http.DefaultTransport = profileTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "Bearer "+*old.AdminToken {
			attempts++
			return nil, errors.New("connection reset")
		}
		return transport.RoundTrip(r)
	})
	captureStdout(t, func() { err = f.run(t, "login", "--profile", "work") })
	http.DefaultTransport = transport
	prefix, _ := tokenPrefix(*old.AdminToken)
	if attempts == 0 {
		t.Fatal("the old administration token was never revoked")
	}
	if err == nil || !strings.Contains(err.Error(), "still live") || !strings.Contains(err.Error(), "sparkwing tokens revoke --profile work --token "+prefix) {
		t.Fatalf("login with a live old administration token = %v, want a failure naming the revoke command", err)
	}
	if code := f.status(t, *old.AdminToken, http.MethodGet, "/api/v1/subscriptions"); code != http.StatusOK {
		t.Fatalf("precondition: the unreached administration token answered %d", code)
	}
	profiles, _, err = loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if c := profiles["work"].Controller; c.Token == nil || *c.Token == *old.Token || c.AdminToken != nil {
		t.Fatalf("the new member login was not saved before the failure: %+v", c)
	}
}
