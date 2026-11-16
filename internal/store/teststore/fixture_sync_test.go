package teststore_test

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestSQLiteFixtureConnectionUsesNormalSynchronization(t *testing.T) {
	st, err := teststore.Open(t, filepath.Join(t.TempDir(), "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	})
	var mode int
	if err := teststore.SQL(t, st).QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 1 {
		t.Fatalf("fixture synchronous=%d, want NORMAL (1)", mode)
	}
}
