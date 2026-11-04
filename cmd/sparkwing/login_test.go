package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

type deviceLoginFixture struct {
	state         *store.Store
	handler       http.Handler
	session, csrf string
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
	previous := deviceLoginSleep
	deviceLoginSleep = func(ctx context.Context, _ time.Duration) error {
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
	}
	t.Cleanup(func() { deviceLoginSleep = previous })
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
		err = runSparkwing([]string{"login", "--profile", "work", "--machine", "desk", "--output", "json"})
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
	newDeviceLoginFixture(t, "deny")
	err := runSparkwing([]string{"login", "--controller", "https://controller.example", "--output", "json"})
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
		err = runRunnersAddArgs([]string{"--profile", "prod", "--runner", "desk", "--personal", "--config", f.config, "--no-service", "--output", "json"})
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
