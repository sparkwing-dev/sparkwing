package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

func TestSchemaV18_ScrubsSecretInputHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret-hash.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, run := range []store.Run{
		{
			ID: "secret", Pipeline: "deploy", Status: "success", StartedAt: time.Now(),
			Args: map[string]string{"token": "low-entropy"},
			Invocation: map[string]any{
				"args":                        map[string]string{"token": "low-entropy"},
				store.InvocationSecretArgsKey: []string{"token"},
			},
		},
		{
			ID: "ordinary", Pipeline: "deploy", Status: "success", StartedAt: time.Now(),
			Invocation: map[string]any{"inputs_hash": "sha256:ordinary"},
		},
		{
			ID: "optional", Pipeline: "deploy", Status: "success", StartedAt: time.Now(),
			Args: map[string]string{"env": "prod"},
			Invocation: map[string]any{
				"args":                        map[string]string{"env": "prod"},
				"inputs_hash":                 "sha256:optional-absent",
				store.InvocationSecretArgsKey: []string{"token"},
			},
		},
		{
			ID: "malformed-classification", Pipeline: "deploy", Status: "success", StartedAt: time.Now(),
			Invocation: map[string]any{},
		},
	} {
		if err := st.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	legacy, err := json.Marshal(map[string]any{
		"args":                        map[string]string{"token": "low-entropy"},
		"inputs_hash":                 "sha256:offline-oracle",
		store.InvocationSecretArgsKey: []string{"token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st, `UPDATE runs SET invocation_json = ? WHERE id = 'secret'`), legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st, `UPDATE runs SET invocation_json = ? WHERE id = 'malformed-classification'`),
		[]byte(`{"inputs_hash":"sha256:malformed","secret_args":"token"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 18`); err != nil {
		t.Fatal(err)
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	var raw []byte
	if err := up.DB().QueryRow(`SELECT invocation_json FROM runs WHERE id = 'secret'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var secret map[string]any
	if err := json.Unmarshal(raw, &secret); err != nil {
		t.Fatal(err)
	}
	if _, ok := secret["inputs_hash"]; ok {
		t.Errorf("secret run retained inputs_hash after migration: %v", secret)
	}

	ordinary, err := up.GetRun(ctx, "ordinary")
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Invocation["inputs_hash"] != "sha256:ordinary" {
		t.Errorf("ordinary inputs_hash = %v, want preserved", ordinary.Invocation["inputs_hash"])
	}
	optional, err := up.GetRun(ctx, "optional")
	if err != nil {
		t.Fatal(err)
	}
	if optional.Invocation["inputs_hash"] != "sha256:optional-absent" {
		t.Errorf("optional-secret-absent inputs_hash = %v, want preserved", optional.Invocation["inputs_hash"])
	}
	malformed, err := up.GetRun(ctx, "malformed-classification")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := malformed.Invocation["inputs_hash"]; ok {
		t.Errorf("malformed classification retained inputs_hash: %v", malformed.Invocation)
	}
}

func TestSchemaV18_FailsClosedOnMalformedInvocationJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(context.Background(), store.Run{
		ID: "broken", Pipeline: "deploy", Status: "success", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE runs SET invocation_json = '{' WHERE id = 'broken'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 18`); err != nil {
		t.Fatal(err)
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Open(path); err == nil || !strings.Contains(err.Error(), "decode run broken invocation_json") {
		t.Fatalf("Open error = %v, want malformed invocation failure", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM sparkwing_schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 17 {
		t.Errorf("schema version = %d after failed migration, want 17", version)
	}
}

func TestCreateRun_RejectsSecretInputHash(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run := store.Run{
		ID: "old-writer", Pipeline: "deploy", Status: "running", StartedAt: time.Now(),
		Args: map[string]string{"token": "guessable"},
		Invocation: map[string]any{
			"inputs_hash":                 "sha256:oracle",
			store.InvocationSecretArgsKey: []string{"token"},
		},
	}
	if err := st.CreateRun(context.Background(), run); !errors.Is(err, store.ErrSecretInputHash) {
		t.Fatalf("CreateRun error = %v, want ErrSecretInputHash", err)
	}
	if _, err := st.GetRun(context.Background(), run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun error = %v, want ErrNotFound", err)
	}
}
