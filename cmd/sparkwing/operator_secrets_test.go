package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestOperatorSecretsRotateResealsThroughTheController(t *testing.T) {
	ctx := t.Context()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := func(fill byte) []byte { return bytes.Repeat([]byte{fill}, secrets.KeySize) }
	old, _ := secrets.NewCipher(key(1))
	next, _ := secrets.NewCipher(key(2))
	rotating, err := secrets.NewCipherWithPrevious(key(2), key(1))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := teststore.ProvisionCredential(ctx, st, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap})
	if err != nil {
		t.Fatal(err)
	}
	value, err := old.SealBound(secrets.PurposeSecret, string(store.DefaultTeam), "TOKEN", "", false, true, "private")
	if err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedSecret(st, store.DefaultTeam, store.Secret{Name: "TOKEN", Masked: true, Principal: "seed", Value: value}, time.Now()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).WithSecretsCipher(rotating).Handler())
	t.Cleanup(srv.Close)
	connect := func(context.Context, string) (controllerwire.OperatorManager, func(), error) {
		m, err := client.NewOperatorManager(client.EngineOptions{URL: srv.URL, HTTP: srv.Client(), Bearer: func(context.Context) (string, error) { return raw, nil }})
		return m, func() {}, err
	}
	inv, err := clisurface.Parse("sparkwing", []string{"operator", "secrets", "rotate", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runOperatorSecretsCommand(ctx, inv, &out, connect); err != nil {
		t.Fatal(err)
	}
	var result controllerwire.SealedRotation
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Secrets != 1 || len(result.Unreadable) != 0 {
		t.Fatalf("rotation output %s: %v", &out, err)
	}
	var stored string
	if err := st.DB().QueryRow(`SELECT value FROM secrets WHERE name='TOKEN'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if plain, err := next.OpenBound(secrets.PurposeSecret, string(store.DefaultTeam), "TOKEN", "", false, true, stored); err != nil || plain != "private" {
		t.Fatalf("the current key alone does not open the rotated secret: %v", err)
	}
	if bytes.Contains(out.Bytes(), []byte("private")) {
		t.Fatal("rotation output carried a value")
	}
}
