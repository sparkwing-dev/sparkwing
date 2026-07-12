package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestMinVersion_FreshOpenStampsBinaryVersion checks that a first open
// records the running binary's version in the sparkwing_meta row the
// skew path later reads.
func TestMinVersion_FreshOpenStampsBinaryVersion(t *testing.T) {
	store.SetBinaryVersion("v0.16.0")
	t.Cleanup(func() { store.SetBinaryVersion("") })

	path := filepath.Join(t.TempDir(), "stamp.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	var got string
	if err := st.DB().QueryRow(
		`SELECT value FROM sparkwing_meta WHERE key = 'min_binary_version'`).Scan(&got); err != nil {
		t.Fatalf("read min_binary_version: %v", err)
	}
	if got != "v0.16.0" {
		t.Errorf("min_binary_version = %q, want v0.16.0", got)
	}
}

// TestMinVersion_SkewMessageNamesVersionAndCommand is the fixture test
// the ticket calls for: a database stamped at a newer schema with a
// recorded minimum version, opened by an older binary, must refuse
// with a message naming the required version and the upgrade command
// rather than a bare schema number.
func TestMinVersion_SkewMessageNamesVersionAndCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skew_stamped.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	future := store.ExpectedSchemaVersion() + 1
	if _, err := st.DB().Exec(
		`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (?, ?)`,
		future, 1); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	if _, err := st.DB().Exec(
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES ('min_binary_version', 'v0.17.0', 1)
		 ON CONFLICT (key) DO UPDATE SET value = 'v0.17.0'`); err != nil {
		t.Fatalf("stamp min version: %v", err)
	}
	_ = st.Close()

	store.SetBinaryVersion("v0.16.0")
	t.Cleanup(func() { store.SetBinaryVersion("") })

	_, err = store.Open(path)
	if err == nil {
		t.Fatal("Open against future-stamped DB should fail")
	}
	var skew *store.SkewError
	if !errors.As(err, &skew) {
		t.Fatalf("err = %v, want *SkewError", err)
	}
	if skew.MinVersion != "v0.17.0" {
		t.Errorf("skew.MinVersion = %q, want v0.17.0", skew.MinVersion)
	}
	if skew.InstalledVersion != "v0.16.0" {
		t.Errorf("skew.InstalledVersion = %q, want v0.16.0", skew.InstalledVersion)
	}
	msg := err.Error()
	for _, want := range []string{"v0.17.0", "v0.16.0", "sparkwing version update --cli"} {
		if !strings.Contains(msg, want) {
			t.Errorf("skew message missing %q; got: %v", want, msg)
		}
	}
}

// TestMinVersion_SkewFallsBackToSchemaNumbers confirms the graceful
// degradation: a database migrated before version stamping shipped
// has no min_binary_version row, so the skew message falls back to the
// schema-number wording.
func TestMinVersion_SkewFallsBackToSchemaNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skew_unstamped.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	future := store.ExpectedSchemaVersion() + 1
	if _, err := st.DB().Exec(
		`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (?, ?)`,
		future, 1); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	if _, err := st.DB().Exec(
		`DELETE FROM sparkwing_meta WHERE key = 'min_binary_version'`); err != nil {
		t.Fatalf("clear min version: %v", err)
	}
	_ = st.Close()

	_, err = store.Open(path)
	var skew *store.SkewError
	if !errors.As(err, &skew) {
		t.Fatalf("err = %v, want *SkewError", err)
	}
	if skew.MinVersion != "" {
		t.Errorf("skew.MinVersion = %q, want empty", skew.MinVersion)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "upgrade sparkwing") {
		t.Errorf("fallback message should mention 'upgrade sparkwing'; got: %v", err)
	}
}

// TestCurrentSchemaVersion_ReportsRecorded verifies the resident-reader
// helper the dashboard polls returns the recorded schema version.
func TestCurrentSchemaVersion_ReportsRecorded(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "current.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	got, err := st.CurrentSchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("CurrentSchemaVersion: %v", err)
	}
	if got != store.ExpectedSchemaVersion() {
		t.Errorf("CurrentSchemaVersion = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}
