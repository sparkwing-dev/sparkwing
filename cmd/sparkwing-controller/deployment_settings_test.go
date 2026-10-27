package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestDeploymentRecordFailurePreservesSettings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.AsOperator().ConfigureDeployment(t.Context(), capability.PaymentTest, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	settings := func() map[string]string {
		t.Helper()
		rows, err := db.QueryContext(t.Context(), "SELECT key,value FROM settings")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		got := map[string]string{}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				t.Fatal(err)
			}
			got[key] = value
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := settings()
	missingDirectory := filepath.Join(root, "absent-directory", "state.db")
	if _, err := configureDeployment(t.Context(), missingDirectory, deployment.Config{}, "", root, st, capability.PaymentLive); err == nil || !strings.Contains(err.Error(), "write deployment record") {
		t.Fatalf("startup with an unwritable deployment record: %v", err)
	}
	if got := settings(); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed startup changed settings: before=%v after=%v", before, got)
	}
	id, err := configureDeployment(t.Context(), path, deployment.Config{}, "", root, st, capability.PaymentLive)
	if err != nil || id == "" {
		t.Fatalf("successful startup: id=%q err=%v", id, err)
	}
	got := settings()
	if got["payment_mode"] != `"live"` || got["payment_deployment"] != `"`+id+`"` {
		t.Fatalf("successful settings: %v", got)
	}
	same, err := configureDeployment(t.Context(), path, deployment.Config{}, "", root, st, "")
	if err != nil || same != id {
		t.Fatalf("restarted deployment: id=%q err=%v", same, err)
	}
	if got := settings(); got["payment_mode"] != `"live"` {
		t.Fatalf("disabled verifier changed payment mode: %v", got)
	}
	before = settings()
	if _, err := configureDeployment(t.Context(), path, deployment.Config{}, "", root, st, capability.PaymentTest); err == nil || !strings.Contains(err.Error(), "live to test") {
		t.Fatalf("test startup after live: %v", err)
	}
	if got := settings(); !reflect.DeepEqual(got, before) {
		t.Fatalf("refused startup changed settings: before=%v after=%v", before, got)
	}
}

func writeDeploymentRecord(ctx context.Context, stateDB string, cfg deployment.Config, credentialsDir, epochDir string, st *store.Store) error {
	body, err := deploymentRecordBytes(ctx, cfg, credentialsDir, epochDir, st)
	if err != nil {
		return err
	}
	return publishDeploymentRecord(stateDB, body)
}

func TestDeploymentCommitFailurePreservesSettingsAndRecord(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "new record", true: "existing record"}[exists], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "state.db")
			st, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			var previous []byte
			if exists {
				if err := writeDeploymentRecord(t.Context(), path, deployment.Config{}, filepath.Join(root, "previous-credentials"), root, st); err != nil {
					t.Fatal(err)
				}
				previous, err = os.ReadFile(deploymentRecordPath(path))
				if err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			for _, statement := range []string{
				`CREATE TABLE startup_fk_parent(id INTEGER PRIMARY KEY)`,
				`CREATE TABLE startup_fk_child(parent INTEGER REFERENCES startup_fk_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
				`CREATE TRIGGER startup_fail_commit AFTER INSERT ON settings WHEN NEW.key='payment_deployment' BEGIN INSERT INTO startup_fk_child(parent) VALUES (1); END`,
			} {
				if _, err := db.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := configureDeployment(t.Context(), path, deployment.Config{}, "", root, st, capability.PaymentLive); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
				t.Fatalf("deferred commit failure: %v", err)
			}
			var count int
			if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM settings").Scan(&count); err != nil || count != 0 {
				t.Fatalf("commit failure changed settings: count=%d, %v", count, err)
			}
			body, err := os.ReadFile(deploymentRecordPath(path))
			if exists {
				if err != nil || !bytes.Equal(body, previous) {
					t.Fatalf("failed startup replaced prior record: %q, %v", body, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("failed startup left a new record: %q, %v", body, err)
			}
		})
	}
}
