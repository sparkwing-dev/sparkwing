package main

import (
	"context"
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

func TestConfigureSecrets_MultiTeamRefusesToStartWithoutAKey(t *testing.T) {
	srv, _ := secretsTestServer(t, license.FeatureMultiTeam)
	if !srv.MultiTeam() {
		t.Fatal("the test license did not make the controller multi-team")
	}
	err := configureSecrets(context.Background(), srv, nil)
	if err == nil {
		t.Fatal("a multi-team controller started without a secrets key")
	}
	if !strings.Contains(err.Error(), "SPARKWING_SECRETS_KEY") {
		t.Fatalf("refusal = %q, want it to name the key setting", err)
	}
}

func TestConfigureSecrets_SingleTeamStartsWithoutAKey(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features []string
	}{
		{"no license", nil},
		{"a license without multi-team", []string{"something-else"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := secretsTestServer(t, tc.features...)
			if err := configureSecrets(context.Background(), srv, nil); err != nil {
				t.Fatalf("a single-team controller refused to start without a key: %v", err)
			}
		})
	}
}

func TestConfigureSecrets_MultiTeamWithAKeySealsPlaintextRows(t *testing.T) {
	srv, st := secretsTestServer(t, license.FeatureMultiTeam)
	if err := st.CreateOrReplaceSecret(store.Secret{
		Name: "TOKEN", Value: "written-before-the-key", Principal: "seed", Masked: true,
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.GenerateKey()
	c, _ := secrets.NewCipher(key)
	if err := configureSecrets(context.Background(), srv, c); err != nil {
		t.Fatalf("configureSecrets: %v", err)
	}
	row, err := st.GetSecret("TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if !secrets.IsBound(row.Value) || strings.Contains(row.Value, "written-before-the-key") {
		t.Fatalf("stored value after start = %.12q..., want a team-bound envelope", row.Value)
	}
}
