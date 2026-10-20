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
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func secretsTestServer(t *testing.T, features ...string) (*controller.Server, *store.Store) {
	t.Helper()
	st, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := controller.New(st, nil)
	if len(features) > 0 {
		pub, priv := licensetest.NewKey(t)
		now := time.Now()
		raw := licensetest.Sign(t, priv, licensetest.Terms{
			Features: features, IssuedTo: "test", IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		})
		lic, err := license.Verify(raw, pub, now)
		if err != nil {
			t.Fatal(err)
		}
		srv.WithLicense(lic)
	}
	return srv, st
}

func postSecret(t *testing.T, srv *controller.Server, st *store.Store, name, value string) (int, string) {
	t.Helper()
	raw, _, err := teststore.ProvisionCredential(t.Context(), st, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "secret-owner"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.TeamAdmin}})
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

func TestConfigureSecrets_MultiTeamRefusesToStartWithoutAKey(t *testing.T) {
	srv, st := secretsTestServer(t, license.FeatureMultiTeam)
	if !srv.MultiTeam() {
		t.Fatal("the test license did not make the controller multi-team")
	}
	err := configureSecrets(context.Background(), srv, st, controllerCredentials{}, keyFileIn(t))
	if err == nil {
		t.Fatal("a multi-team controller started without a secrets key")
	}
	if !strings.Contains(err.Error(), credSecretsKey) {
		t.Fatalf("refusal = %q, want it to name the key setting", err)
	}
}

func TestConfigureSecrets_SingleTeamSQLiteCreatesItsKeyOnTheFirstSecret(t *testing.T) {
	srv, st := secretsTestServer(t)
	keyPath := keyFileIn(t)
	if err := configureSecrets(context.Background(), srv, st, controllerCredentials{}, keyPath); err != nil {
		t.Fatalf("a single-team controller refused to start without a key: %v", err)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("stat %s = %v, want no key before any secret is written", keyPath, err)
	}
	if code, _ := postSecret(t, srv, st, "TOKEN", "first-value"); code != http.StatusNoContent {
		t.Fatalf("POST secret = %d, want 204", code)
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file %s = %v, %v; want one with mode 0600 after the first secret", keyPath, info, err)
	}
	row, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretRow("TOKEN", "")
	if err != nil || !secrets.IsBound(row.Value) {
		t.Fatalf("stored value = %v, %v; want a sealed envelope", row, err)
	}

	restarted := controller.New(st, nil)
	if err := configureSecrets(context.Background(), restarted, st, controllerCredentials{}, keyPath); err != nil {
		t.Fatalf("restart with the generated key: %v", err)
	}
	if code, _ := postSecret(t, restarted, st, "OTHER", "second-value"); code != http.StatusNoContent {
		t.Fatalf("POST after restart = %d, want 204", code)
	}
}

func TestConfigureSecrets_SingleTeamSQLiteRefusesASecondKeyForSealedRows(t *testing.T) {
	srv, st := secretsTestServer(t)
	keyPath := keyFileIn(t)
	if err := configureSecrets(context.Background(), srv, st, controllerCredentials{}, keyPath); err != nil {
		t.Fatal(err)
	}
	if code, _ := postSecret(t, srv, st, "TOKEN", "v"); code != http.StatusNoContent {
		t.Fatalf("POST secret = %d", code)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	err := configureSecrets(context.Background(), controller.New(st, nil), st, controllerCredentials{}, keyPath)
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
