package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// safety: an older binary refuses an upgraded store, so the copy taken here is
// the only way back to it. The upgrade transaction already holds the write lock,
// so this read-only snapshot is exactly the state the upgrade starts from.
func backupSQLiteBeforeUpgrade(ctx context.Context, path string, version int) error {
	final := fmt.Sprintf("%s.schema-%d-%s.bak", path, version, time.Now().UTC().Format("20060102T150405Z"))
	if err := copySQLiteSnapshot(ctx, path, final); err != nil {
		return fmt.Errorf("back up the store to %s before upgrading schema %d: %w; the store is unchanged", final, version, err)
	}
	slog.Info("backed up the store before upgrading its schema", "backup", final, "from_schema", version, "to_schema", expectedSchemaVersion)
	return nil
}

// safety: the copy is built in a temporary directory beside the store, so an
// interrupted backup never looks complete, SQLite's sidecar files go with the
// directory, and the copy inherits the same directory ACL that protects
// state.db on Windows. Link refuses to replace an earlier backup.
func copySQLiteSnapshot(ctx context.Context, path, final string) (err error) {
	dir, err := os.MkdirTemp(filepath.Dir(path), ".sparkwing-backup-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	partial := filepath.Join(dir, "state.db")
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("%s?mode=ro&_pragma=busy_timeout(%d)", sqliteURI(path), busyTimeoutMS))
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `VACUUM INTO ?`, partial)
	if err := errors.Join(err, db.Close()); err != nil {
		return err
	}
	return os.Link(partial, final)
}

func pruneSQLiteSchemaBackups(path string) error {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	var backups []os.FileInfo
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), filepath.Base(path)+".schema-") || !strings.HasSuffix(entry.Name(), ".bak") || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		backups = append(backups, info)
	}
	slices.SortFunc(backups, func(a, b os.FileInfo) int {
		if order := b.ModTime().Compare(a.ModTime()); order != 0 {
			return order
		}
		return strings.Compare(b.Name(), a.Name())
	})
	for _, backup := range backups[min(2, len(backups)):] {
		if err := os.Remove(filepath.Join(filepath.Dir(path), backup.Name())); err != nil {
			return err
		}
	}
	return nil
}
