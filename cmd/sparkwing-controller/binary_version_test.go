package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A release controller stamps its own version on the migrations it runs, so
// an operator reading the database can tell which build last upgraded it.
func TestControllerStampsItsReleaseVersionOnMigrations(t *testing.T) {
	saved := Version
	t.Cleanup(func() {
		Version = saved
		store.SetBinaryVersion("")
	})
	Version = "v0.61.0-stamp-test"
	stampBinaryVersion()

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if got := st.MinBinaryVersion(context.Background()); got != Version {
		t.Fatalf("min_binary_version = %q, want %q", got, Version)
	}
	rows, err := st.DB().Query(`SELECT name, added_by_version FROM sparkwing_requirements`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	seen := 0
	for rows.Next() {
		var name, addedBy string
		if err := rows.Scan(&name, &addedBy); err != nil {
			t.Fatal(err)
		}
		seen++
		if addedBy != Version {
			t.Errorf("requirement %s added_by_version = %q, want %q", name, addedBy, Version)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("a fresh store recorded no requirements, so the stamp went unchecked")
	}
}
