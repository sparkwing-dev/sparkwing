//go:build linux

package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// safety: the file size limit applies to the whole process, so the write
// failure runs in a child copy of this test binary.
func TestBackupWriteFailureRefusesUpgradeAndLeavesNothing(t *testing.T) {
	if os.Getenv("SPARKWING_BACKUP_FSIZE_CHILD") == "" {
		command := exec.Command(os.Args[0], "-test.run=^TestBackupWriteFailureRefusesUpgradeAndLeavesNothing$")
		command.Env = append(os.Environ(), "SPARKWING_BACKUP_FSIZE_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("write failure child: %v\n%s", err, output)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE github_actions_dispatches`,
		`DROP TABLE github_actions_approvals`,
		`UPDATE sparkwing_schema_version SET version=34, baseline_digest='187573d27274e9bf1796a84d502b39ac9e8c0cb7730e41143a1e4f586d5a6012'`,
		`CREATE TABLE backup_probe(data BLOB)`,
		`INSERT INTO backup_probe VALUES(zeroblob(2097152))`,
	} {
		if _, err := st.DB().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	// safety: closing would checkpoint and restart the WAL, and the limited
	// process could then not even begin the upgrade transaction.
	defer st.Close()
	var previous syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &previous); err != nil {
		t.Fatal(err)
	}
	limited := previous
	limited.Cur = 8192
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if restoreErr := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &previous); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if err == nil {
		_ = upgraded.Close()
		t.Fatal("upgraded without a backup")
	}
	if !strings.Contains(err.Error(), "back up the store") || !strings.Contains(err.Error(), "the store is unchanged") {
		t.Fatalf("refusal = %v", err)
	}
	if err := ExecRawForTest(t.Context(), DialectSQLite, path, `SELECT data FROM backup_probe`); err != nil {
		t.Fatalf("refused store lost data: %v", err)
	}
	if ExecRawForTest(t.Context(), DialectSQLite, path, `SELECT receipt FROM github_actions_dispatches`) == nil {
		t.Fatal("refused store was upgraded")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "state.db", "state.db-wal", "state.db-shm":
		default:
			t.Errorf("failed backup left %s", entry.Name())
		}
	}
}
