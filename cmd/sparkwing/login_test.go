package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type deviceLoginFixture struct {
	state         *store.Store
	handler       http.Handler
	session, csrf string
	console       deviceConsole
}

func (f *deviceLoginFixture) run(t *testing.T, args ...string) error {
	t.Helper()
	inv, err := clisurface.Parse("sparkwing", args)
	if err != nil {
		return err
	}
	if err := applyConfiguredProfile(&inv); err != nil {
		return err
	}
	if inv.Command.Path == "sparkwing login" {
		return login(inv, f.console)
	}
	return runnersAdd(inv, f.console)
}

func newDeviceLoginFixture(t *testing.T, decision string) *deviceLoginFixture {
	t.Helper()
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	var server *controller.Server
	state, err := teststore.Open(t, filepath.Join(t.TempDir(), "state.db"), store.WithPolicies(func(state *store.Store) (store.Policies, error) {
		server = controller.NewDeployment(state, deploymenttest.Team(t), nil).WithSecretsCipher(cipher)
		return server.StorePolicies(), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	teststore.SeedTeam(t, state, "device-test")
	for _, query := range []string{
		`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`,
		`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('device-test','member','editor',1)`,
	} {
		if _, err := teststore.SQL(t, state).ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	f := &deviceLoginFixture{state: state, handler: server.Handler()}
	f.session, f.csrf, _, err = state.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "device-test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	http.DefaultTransport = profileTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		return w.Result(), nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	f.console = deviceConsole{interactive: true, sleep: func(ctx context.Context, _ time.Duration) error {
		db := teststore.SQL(t, state)
		if _, err := db.ExecContext(ctx, `UPDATE device_logins SET polled_at = polled_at - 120000000000 WHERE polled_at IS NOT NULL`); err != nil {
			return err
		}
		var code string
		err := db.QueryRowContext(ctx, `SELECT user_code FROM device_logins WHERE state='pending'`).Scan(&code)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if status := f.browser(t, decision, code); status != http.StatusNoContent {
			t.Errorf("browser %s answered %d", decision, status)
		}
		return nil
	}}
	return f
}

func (f *deviceLoginFixture) browser(t *testing.T, decision, code string) int {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "https://controller.example/api/v1/auth/device/"+decision, strings.NewReader(`{"user_code":"`+code+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://controller.example")
	r.Header.Set("Authorization", "Session "+f.session)
	r.Header.Set("X-CSRF-Token", f.csrf)
	r.AddCookie(&http.Cookie{Name: "__Host-sw_csrf", Value: f.csrf})
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w.Code
}

func TestLoginSavesTheTokenTheBrowserApproved(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  work:\n    controller: { url: https://controller.example }\n    default: true\n")
	f := newDeviceLoginFixture(t, "approve")
	var err error
	out := captureStdout(t, func() {
		err = f.run(t, "login", "--profile", "work", "--machine", "desk", "--team", "device-test", "--output", "json")
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	profiles, _, err := loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	saved := profiles["work"]
	if saved.Controller.URL != "https://controller.example" || saved.Controller.Token == nil || !saved.Default {
		t.Fatalf("saved profile = %+v", saved)
	}
	if strings.Contains(out, *saved.Controller.Token) {
		t.Fatalf("login printed the bearer: %s", out)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/whoami", nil)
	r.Header.Set("Authorization", "Bearer "+*saved.Controller.Token)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("saved token whoami = %d %s", w.Code, w.Body)
	}
	var kind, grants string
	if err := teststore.SQL(t, f.state).QueryRowContext(t.Context(), `SELECT subject_kind, grants FROM capabilities WHERE subject_id='member' AND parent_kind='session'`).Scan(&kind, &grants); err != nil || kind != "account" || strings.Contains(grants, "team.admin") {
		t.Fatalf("minted credential %s %s %v", kind, grants, err)
	}
}

func TestLoginReportsADeniedApproval(t *testing.T) {
	writeProfilesFixture(t, "profiles: {}\n")
	f := newDeviceLoginFixture(t, "deny")
	err := f.run(t, "login", "--controller", "https://controller.example", "--output", "json")
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("denied login: %v", err)
	}
	profiles, _, loadErr := loadProfiles()
	if loadErr != nil || len(profiles) != 0 {
		t.Fatalf("denied login saved a profile: %+v %v", profiles, loadErr)
	}
}

func TestRunnersAddEnrollsThroughTheBrowser(t *testing.T) {
	f := newRunnersFixture(t)
	device := newDeviceLoginFixture(t, "approve")
	var err error
	captureStdout(t, func() {
		err = device.run(t, "runners", "add", "--profile", "prod", "--runner", "desk", "--personal", "--config", f.config, "--no-service", "--output", "json")
	})
	if err != nil {
		t.Fatalf("browser enrollment: %v", err)
	}
	cfg, err := userconfig.ReadConfig(f.config)
	if err != nil || cfg.Runner == nil {
		t.Fatalf("enrollment configuration: %+v %v", cfg, err)
	}
	var personal int
	if err := teststore.SQL(t, device.state).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities WHERE subject_kind='machine' AND subject_id='desk'`).Scan(&personal); err != nil || personal != 1 {
		t.Fatalf("enrolled machine credentials %d %v", personal, err)
	}
}

func TestBrowserApprovalRefusesScriptsAndSharedRunners(t *testing.T) {
	f := newRunnersFixture(t)
	device := newDeviceLoginFixture(t, "approve")
	device.console.interactive = false
	if err := device.run(t, "login", "--controller", "https://controller.example", "--profile", "ci"); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("non-terminal login: %v", err)
	}
	device.console.interactive = true
	err := device.run(t, "runners", "add", "--profile", "prod", "--runner", "desk", "--repos", "github.com/acme/*", "--config", f.config, "--no-service")
	if err == nil || !strings.Contains(err.Error(), "--session-stdin") {
		t.Fatalf("shared browser enrollment: %v", err)
	}
	var started int
	if err := teststore.SQL(t, device.state).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM device_logins`).Scan(&started); err != nil || started != 0 {
		t.Fatalf("refused commands started %d device logins %v", started, err)
	}
}

func TestLoginWithControllerIgnoresTheSavedDefaultProfile(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  work:\n    controller: { url: https://elsewhere.example }\n    default: true\n")
	f := newDeviceLoginFixture(t, "approve")
	var err error
	captureStdout(t, func() { err = f.run(t, "login", "--controller", "https://controller.example", "--output", "json") })
	if err != nil {
		t.Fatalf("login beside a default profile: %v", err)
	}
	profiles, _, err := loadProfiles()
	if err != nil || profiles["work"].Controller.URL != "https://elsewhere.example" || profiles["controller-example"].Controller.Token == nil {
		t.Fatalf("profiles after login = %+v %v", profiles, err)
	}
}

func TestLoginWarnsOnlyWhenPreviousTokenRevocationIsUnconfirmed(t *testing.T) {
	for _, tc := range []struct {
		name, method  string
		code          apierr.Code
		warn, revoked bool
	}{
		{name: "already revoked", revoked: true},
		{name: "live revoked"},
		{name: "not live", method: http.MethodGet, code: hosterr.Unauthenticated},
		{name: "lookup absent", method: http.MethodGet, code: apierr.NotFound},
		{name: "ended during revoke", method: http.MethodDelete, code: hosterr.Unauthenticated},
		{name: "absent during revoke", method: http.MethodDelete, code: apierr.NotFound},
		{name: "live revoke failed", method: http.MethodDelete, code: hosterr.Unavailable, warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeProfilesFixture(t, "profiles: {}\n")
			f := newDeviceLoginFixture(t, "approve")
			var err error
			captureStdout(t, func() {
				err = f.run(t, "login", "--profile", "work", "--controller", "https://controller.example", "-o", "json")
			})
			if err != nil {
				t.Fatal(err)
			}
			profiles, _, err := loadProfiles()
			if err != nil {
				t.Fatal(err)
			}
			previous := *profiles["work"].Controller.Token
			if tc.revoked {
				if err := revokeProfileToken(&namedProfile{Profile: profiles["work"]}); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			original := f.handler
			f.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.method != "" && r.Method == tc.method && strings.HasPrefix(r.URL.Path, "/api/v1/tokens/") {
					calls++
					w.Header().Set("Content-Type", "application/json")
					status := http.StatusServiceUnavailable
					if tc.code == apierr.NotFound {
						status = http.StatusNotFound
					}
					if tc.code == hosterr.Unauthenticated {
						status = http.StatusUnauthorized
					}
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(hosterr.Envelope{Error: &apierr.Error{Code: tc.code, Message: "token cleanup refused"}})
					return
				}
				original.ServeHTTP(w, r)
			})
			stderr := captureStderr(t, func() {
				captureStdout(t, func() { err = f.run(t, "login", "--profile", "work", "--team", "device-test", "-o", "json") })
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.method != "" && calls != 1 {
				t.Fatalf("cleanup calls = %d", calls)
			}
			if warned := strings.Contains(stderr, "previous token was not revoked"); warned != tc.warn {
				t.Fatalf("warning = %v, want %v: %s", warned, tc.warn, stderr)
			}
			profiles, _, err = loadProfiles()
			if err != nil || profiles["work"].Controller.Token == nil || *profiles["work"].Controller.Token == previous {
				t.Fatalf("replacement was not saved: %v", err)
			}
		})
	}
}

func TestLoginDefaultsToSavedProfileTeam(t *testing.T) {
	writeProfilesFixture(t, "profiles: {}\n")
	f := newDeviceLoginFixture(t, "approve")
	if err := f.run(t, "login", "--controller", "https://controller.example", "--profile", "work", "--output", "plain"); err != nil {
		t.Fatal(err)
	}
	assertLoginRequestedTeam(t, f, "device-test", "login", "--profile", "work", "--output", "plain")
}

func TestLoginWithoutProfileLeavesTeamUnspecified(t *testing.T) {
	writeProfilesFixture(t, "profiles: {}\n")
	f := newDeviceLoginFixture(t, "approve")
	assertLoginRequestedTeam(t, f, "", "login", "--controller", "https://controller.example", "--output", "plain")
}

func assertLoginRequestedTeam(t *testing.T, f *deviceLoginFixture, want string, args ...string) {
	t.Helper()
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	started := false
	http.DefaultTransport = profileTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/device" {
			started = true
			var request controllerwire.StartDeviceLoginRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Team != want {
				t.Fatalf("requested team = %q, want %q", request.Team, want)
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			r.Body = io.NopCloser(strings.NewReader(string(body)))
		}
		return transport.RoundTrip(r)
	})
	if err := f.run(t, args...); err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("device sign-in was not started")
	}
}
