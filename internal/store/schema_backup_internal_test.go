package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupNeverReplacesAnEarlierBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	earlier := path + ".schema-32-earlier.bak"
	if err := os.WriteFile(earlier, []byte("earlier backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copySQLiteSnapshot(t.Context(), path, earlier); err == nil {
		t.Fatal("backup replaced an earlier one")
	}
	if data, err := os.ReadFile(earlier); err != nil || string(data) != "earlier backup" {
		t.Fatalf("earlier backup = %q, %v; want it untouched", data, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".sparkwing-backup-*")); len(leftovers) != 0 {
		t.Fatalf("refused backup left %v", leftovers)
	}
}
