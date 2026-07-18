package controller_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func healthAuthField(t *testing.T, base string) string {
	t.Helper()
	resp, err := http.Get(base + "/api/v1/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	auth, _ := body["auth"].(string)
	return auth
}

func TestController_Health_ReportsAuthDisabledWhenTokensEmpty(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()

	if got := healthAuthField(t, srv.URL); got != "disabled" {
		t.Fatalf("auth=%q want disabled", got)
	}
}

func TestController_Health_ReportsAuthEnabledWhenTokenPresent(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	if _, _, err := st.CreateToken("admin", store.TokenKindUser,
		[]string{"admin"}, 0, time.Now().UTC()); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()

	if got := healthAuthField(t, srv.URL); got != "enabled" {
		t.Fatalf("auth=%q want enabled", got)
	}
}

func TestEnableAuthFromStore_WarnsWhenTokensEmpty(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	s := controller.New(st, logger).EnableAuthFromStore()
	if s.AuthEnabled() {
		t.Fatal("AuthEnabled() = true, want false for empty tokens table")
	}
	if !strings.Contains(buf.String(), "serving unauthenticated") {
		t.Fatalf("expected open-serving warning, got logs: %q", buf.String())
	}
}

func TestEnableAuthFromStore_SilentAndEnabledWhenTokenPresent(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	if _, _, err := st.CreateToken("admin", store.TokenKindUser,
		[]string{"admin"}, 0, time.Now().UTC()); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	s := controller.New(st, logger).EnableAuthFromStore()
	if !s.AuthEnabled() {
		t.Fatal("AuthEnabled() = false, want true when a token exists")
	}
	if strings.Contains(buf.String(), "serving unauthenticated") {
		t.Fatalf("unexpected open-serving warning with a token present: %q", buf.String())
	}
}
