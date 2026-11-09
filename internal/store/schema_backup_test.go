package store_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestSQLiteUpgradeKeepsABackupTheOlderBinaryCanOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	addSchema35BuildTrust(t, st)
	dropActionsSchema(t, st)
	for _, statement := range []string{
		`DROP TABLE local_runner_instances`,
		`DROP INDEX idx_runs_live_children`,
		`UPDATE sparkwing_schema_version SET version=32, baseline_digest='8229dac48bd08544fb5b2cddfe2f9c9d0f0f3c41968eda4db1591aff9f247f61'`,
		`INSERT INTO teams(name,created_at,updated_at) VALUES ('kept',1,1)`,
	} {
		if _, err := st.DB().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(path + ".schema-32-*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v, %v; want one schema 32 copy", backups, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".sparkwing-backup-*")); len(leftovers) != 0 {
		t.Fatalf("partial backups left behind: %v", leftovers)
	}
	info, err := os.Stat(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, want 0600", info.Mode().Perm())
	}
	db, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, teams, runners int
	if err := db.QueryRow(`SELECT version FROM sparkwing_schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM teams WHERE name='kept'`).Scan(&teams); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='local_runner_instances'`).Scan(&runners); err != nil {
		t.Fatal(err)
	}
	if version != 32 || teams != 1 || runners != 0 {
		t.Fatalf("backup holds version %d, %d kept teams, %d runner tables; want the schema 32 store as it was", version, teams, runners)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if again, _ := filepath.Glob(path + ".schema-*.bak"); len(again) != 1 {
		t.Fatalf("opening a current store took another backup: %v", again)
	}
}

func TestSQLiteRefusedUpgradeTakesNoBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE sparkwing_schema_version SET version=32, baseline_digest='other'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if refused, err := store.Open(path); err == nil {
		_ = refused.Close()
		t.Fatal("opened a store whose baseline this binary does not upgrade")
	}
	if backups, _ := filepath.Glob(path + ".schema-*"); len(backups) != 0 {
		t.Fatalf("a refused upgrade left backups: %v", backups)
	}
}

func TestSQLiteUpgradePrunesBackupsOnlyAfterSuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed=%t", fail), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			st, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			addSchema35BuildTrust(t, st)
			dropActionsSchema(t, st)
			if !fail {
				if _, err := st.DB().Exec(`DROP INDEX idx_runs_live_children`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.DB().Exec(`UPDATE sparkwing_schema_version SET version=33, baseline_digest='b4ac52448bafd4ea378d1ed2e1d9aec0c610d3a5e27df4a7a780c178f425a47b'`); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			for i, name := range []string{"state.db.schema-9-old.bak", "state.db.schema-10-middle.bak", "state.db.schema-32-newest.bak", "other.db.schema-1-old.bak", "state.db.schema-partial"} {
				backup := filepath.Join(filepath.Dir(path), name)
				if err := os.WriteFile(backup, []byte(name), 0o600); err != nil {
					t.Fatal(err)
				}
				stamp := time.Unix(int64(i+1), 0)
				if err := os.Chtimes(backup, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			directory := path + ".schema-directory.bak"
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			upgraded, err := store.Open(path)
			if fail {
				if err == nil {
					_ = upgraded.Close()
					t.Fatal("upgrade with an existing index succeeded")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := upgraded.Close(); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"state.db.schema-9-old.bak", "state.db.schema-10-middle.bak", "state.db.schema-32-newest.bak", "other.db.schema-1-old.bak", "state.db.schema-partial", "state.db.schema-directory.bak"} {
				_, err := os.Stat(filepath.Join(filepath.Dir(path), name))
				removed := !fail && (strings.Contains(name, "9-old") || strings.Contains(name, "10-middle"))
				if removed && !os.IsNotExist(err) || !removed && err != nil {
					t.Errorf("%s: %v, removed=%t", name, err, removed)
				}
			}
		})
	}
}
