package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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
	row, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretRow("TOKEN", "")
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

func TestKeylessCipherRefusesCredentialAccess(t *testing.T) {
	cipher := keylessCipher{}
	for _, access := range []func() (string, error){
		func() (string, error) { return cipher.SealBound("team", "TOKEN", "build", false, true, "private") },
		func() (string, error) { return cipher.OpenBound("team", "TOKEN", "build", false, true, "sealed") },
	} {
		value, err := access()
		if value != "" || !errors.Is(err, secrets.ErrKeyRefused) || !strings.Contains(err.Error(), credSecretsKey) {
			t.Fatalf("keyless credential access: %q, %v", value, err)
		}
	}
}
