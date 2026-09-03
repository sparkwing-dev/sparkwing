package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSQLiteDSNEscapesURIMetacharactersInPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"fragment", "/tmp/a#b/state.db", "/tmp/a%23b/state.db"},
		{"query", "/tmp/q?x/state.db", "/tmp/q%3Fx/state.db"},
		{"percent", "/tmp/pct%41x/state.db", "/tmp/pct%2541x/state.db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rw, err := sqliteDSN(tc.path)
			if err != nil {
				t.Fatalf("sqliteDSN: %v", err)
			}
			ro, err := sqliteReadOnlyDSN(tc.path)
			if err != nil {
				t.Fatalf("sqliteReadOnlyDSN: %v", err)
			}
			for _, dsn := range []string{rw, ro} {
				filename, params, ok := strings.Cut(dsn, "?")
				if !ok {
					t.Fatalf("DSN %q has no query string", dsn)
				}
				if filename != "file:"+tc.want {
					t.Errorf("DSN %q names %q, want file:%s", dsn, filename, tc.want)
				}
				if !strings.Contains(params, "_pragma=busy_timeout(") {
					t.Errorf("DSN %q lost its pragmas", dsn)
				}
			}
		})
	}
}

func TestOpenWritesTheStatDatabaseWhenPathHasURIMetacharacters(t *testing.T) {
	for _, dirName := range []string{"a#b", "q?x", "pct%41x"} {
		t.Run(dirName, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, dirName)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			path := filepath.Join(dir, "state.db")

			st, err := Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := st.CreateRun(t.Context(), Run{
				ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
			}); err != nil {
				_ = st.Close()
				t.Fatalf("CreateRun: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("read root: %v", err)
			}
			if len(entries) != 1 || entries[0].Name() != dirName {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Fatalf("root holds %v, want only %q -- SQLite opened a path the caller never named", names, dirName)
			}

			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat %s: %v", path, err)
			}
			if info.Size() == 0 {
				t.Fatalf("%s is empty; the schema was written somewhere else", path)
			}
			if runtime.GOOS != "windows" {
				if got := info.Mode().Perm(); got != 0o600 {
					t.Fatalf("%s mode = %04o, want 0600", path, got)
				}
			}

			reopened, err := Open(path)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			defer func() { _ = reopened.Close() }()
			run, err := reopened.GetRun(t.Context(), "run-1")
			if err != nil {
				t.Fatalf("GetRun: %v", err)
			}
			if run.Pipeline != "demo" {
				t.Fatalf("GetRun pipeline = %q, want demo", run.Pipeline)
			}
		})
	}
}

func TestOpenAppliesPragmasWhenPathHasURIMetacharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a#b")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	st, err := Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	var foreignKeys int
	if err := st.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
	var journal string
	if err := st.DB().QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
}
