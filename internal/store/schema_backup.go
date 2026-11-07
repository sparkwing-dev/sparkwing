package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
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

// safety: the copy is written under a temporary name so an interrupted
// backup never looks complete, and Link refuses to replace an earlier backup.
func copySQLiteSnapshot(ctx context.Context, path, final string) (err error) {
	partial := final + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(partial)) }()
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
