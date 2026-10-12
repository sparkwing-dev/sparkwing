package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func secretsTestServer(t *testing.T, features ...string) (*controller.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
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

func postSecret(t *testing.T, srv *controller.Server, name, value string) (int, string) {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	body, _ := json.Marshal(map[string]string{"name": name, "value": value})
	resp, err := http.Post(ts.URL+"/api/v1/secrets", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(reply)
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
	if code, _ := postSecret(t, srv, "TOKEN", "first-value"); code != http.StatusNoContent {
		t.Fatalf("POST secret = %d, want 204", code)
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file %s = %v, %v; want one with mode 0600 after the first secret", keyPath, info, err)
	}
	row, err := st.GetSecret("TOKEN")
	if err != nil || !secrets.IsBound(row.Value) {
		t.Fatalf("stored value = %v, %v; want a sealed envelope", row, err)
	}

	restarted := controller.New(st, nil)
	if err := configureSecrets(context.Background(), restarted, st, controllerCredentials{}, keyPath); err != nil {
		t.Fatalf("restart with the generated key: %v", err)
	}
	if code, _ := postSecret(t, restarted, "OTHER", "second-value"); code != http.StatusNoContent {
		t.Fatalf("POST after restart = %d, want 204", code)
	}
}

func TestConfigureSecrets_SingleTeamSQLiteRefusesASecondKeyForSealedRows(t *testing.T) {
	srv, st := secretsTestServer(t)
	keyPath := keyFileIn(t)
	if err := configureSecrets(context.Background(), srv, st, controllerCredentials{}, keyPath); err != nil {
		t.Fatal(err)
	}
	if code, _ := postSecret(t, srv, "TOKEN", "v"); code != http.StatusNoContent {
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

func TestConfigureSecrets_PlaintextRowsAreSealedAtStartup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features []string
		creds    func(t *testing.T) controllerCredentials
	}{
		{"multi-team with a key", []string{license.FeatureMultiTeam}, func(t *testing.T) controllerCredentials {
			key, err := secrets.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			return controllerCredentials{SecretsKey: key}
		}},
		{"single-team SQLite without a key", nil, func(*testing.T) controllerCredentials { return controllerCredentials{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, st := secretsTestServer(t, tc.features...)
			if err := st.CreateOrReplaceSecret(store.Secret{
				Name: "TOKEN", Value: "written-before-the-key", Principal: "seed", Masked: true,
			}, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if err := configureSecrets(context.Background(), srv, st, tc.creds(t), keyFileIn(t)); err != nil {
				t.Fatalf("configureSecrets: %v", err)
			}
			row, err := st.GetSecret("TOKEN")
			if err != nil {
				t.Fatal(err)
			}
			if !secrets.IsBound(row.Value) || strings.Contains(row.Value, "written-before-the-key") {
				t.Fatalf("stored value after start = %.12q..., want a team-bound envelope", row.Value)
			}
		})
	}
}

func TestKeylessControllerRefusesSecretWritesAndPlaintextRows(t *testing.T) {
	srv, st := secretsTestServer(t)
	srv.WithSecretsCipher(keylessCipher{})
	code, body := postSecret(t, srv, "TOKEN", "v")
	if code != http.StatusConflict || !strings.Contains(body, credSecretsKey) {
		t.Fatalf("POST secret = %d %s, want 409 naming the %s credential", code, body, credSecretsKey)
	}
	if err := refusePlaintextSecrets(context.Background(), st); err != nil {
		t.Fatalf("an empty store refused: %v", err)
	}
	if err := st.CreateOrReplaceSecret(store.Secret{Name: "OLD", Value: "plain", Principal: "seed"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := refusePlaintextSecrets(context.Background(), st); err == nil || !strings.Contains(err.Error(), "1 secrets as plaintext") {
		t.Fatalf("plaintext row = %v, want a refusal counting it", err)
	}
}
