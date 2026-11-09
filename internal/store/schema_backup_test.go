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
		`UPDATE sparkwing_schema_version SET version=34, baseline_digest='187573d27274e9bf1796a84d502b39ac9e8c0cb7730e41143a1e4f586d5a6012'`,
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
	backups, err := filepath.Glob(path + ".schema-34-*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v, %v; want one schema 34 copy", backups, err)
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
	var version, teams, dispatches int
	if err := db.QueryRow(`SELECT version FROM sparkwing_schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM teams WHERE name='kept'`).Scan(&teams); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='github_actions_dispatches'`).Scan(&dispatches); err != nil {
		t.Fatal(err)
	}
	if version != 34 || teams != 1 || dispatches != 0 {
		t.Fatalf("backup holds version %d, %d kept teams, %d dispatch tables; want the schema 34 store as it was", version, teams, dispatches)
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
	if _, err := st.DB().Exec(`UPDATE sparkwing_schema_version SET version=34, baseline_digest='other'`); err != nil {
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
			if !fail {
				dropActionsSchema(t, st)
			}
			if _, err := st.DB().Exec(`UPDATE sparkwing_schema_version SET version=34, baseline_digest='187573d27274e9bf1796a84d502b39ac9e8c0cb7730e41143a1e4f586d5a6012'`); err != nil {
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
					t.Fatal("upgrade with existing Actions tables succeeded")
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
