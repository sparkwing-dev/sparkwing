package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func secretsTestServer(t *testing.T, cfg deployment.Config) (*controller.Server, *store.Store) {
	t.Helper()
	st, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return controller.NewDeployment(st, cfg, nil), st
}

func postSecret(t *testing.T, srv *controller.Server, st *store.Store, name, value string) (int, string) {
	t.Helper()
	raw, _, err := teststore.ProvisionCredential(t.Context(), st, capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "secret-owner"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.TeamAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"name": name, "value": value})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/secrets", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func keyFileIn(t *testing.T) string {
	t.Helper()
	return generatedKeyPath(filepath.Join(t.TempDir(), "state.db"))
}

func TestParseSecretsCipherMultiTeamRefusesToStartWithoutAKey(t *testing.T) {
	cfg := deploymenttest.Team(t)
	_, err := parseSecretsCipher(cfg)
	if err == nil {
		t.Fatal("a multi-team controller started without a secrets key")
	}
	if !strings.Contains(err.Error(), credSecretsKey) {
		t.Fatalf("refusal = %q, want it to name the key setting", err)
	}
}

func TestConfigureSecrets_SingleTeamSQLiteCreatesItsKeyOnTheFirstSecret(t *testing.T) {
	srv, st := secretsTestServer(t, deployment.Config{})
	keyPath := keyFileIn(t)
	if err := configureSecrets(context.Background(), srv, st, nil, keyPath); err != nil {
		t.Fatalf("a single-team controller refused to start without a key: %v", err)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("stat %s = %v, want no key before any secret is written", keyPath, err)
	}
	if code, _ := postSecret(t, srv, st, "TOKEN", "first-value"); code != http.StatusOK {
		t.Fatalf("POST secret = %d, want 200", code)
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file %s = %v, %v; want one with mode 0600 after the first secret", keyPath, info, err)
	}
	row, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretRow("TOKEN", "", "")
	if err != nil || !secrets.IsBound(row.Value) {
		t.Fatalf("stored value = %v, %v; want a sealed envelope", row, err)
	}

	restarted := controller.New(st, nil)
	if err := configureSecrets(context.Background(), restarted, st, nil, keyPath); err != nil {
		t.Fatalf("restart with the generated key: %v", err)
	}
	if code, _ := postSecret(t, restarted, st, "OTHER", "second-value"); code != http.StatusOK {
		t.Fatalf("POST after restart = %d, want 200", code)
	}
}

func TestConfigureSecrets_SingleTeamSQLiteRefusesASecondKeyForSealedRows(t *testing.T) {
	srv, st := secretsTestServer(t, deployment.Config{})
	keyPath := keyFileIn(t)
	if err := configureSecrets(context.Background(), srv, st, nil, keyPath); err != nil {
		t.Fatal(err)
	}
	if code, _ := postSecret(t, srv, st, "TOKEN", "private-value"); code != http.StatusOK {
		t.Fatalf("POST secret = %d", code)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	err := configureSecrets(context.Background(), controller.New(st, nil), st, nil, keyPath)
	if err == nil || !strings.Contains(err.Error(), keyPath) {
		t.Fatalf("start after losing the key = %v, want a refusal naming %s", err, keyPath)
	}
}

func TestKeylessCipherRefusesCredentialAccess(t *testing.T) {
	cipher := keylessCipher{}
	for _, access := range []func() (string, error){
		func() (string, error) {
			return cipher.SealBound(secrets.PurposeSecret, "team", "TOKEN", "build", false, true, "private")
		},
		func() (string, error) {
			return cipher.OpenBound(secrets.PurposeSecret, "team", "TOKEN", "build", false, true, "sealed")
		},
	} {
		value, err := access()
		if value != "" || !errors.Is(err, secrets.ErrKeyRefused) || !strings.Contains(err.Error(), credSecretsKey) {
			t.Fatalf("keyless credential access: %q, %v", value, err)
		}
	}
}
