package store

// hack: these methods serve the one-time import of the local dotenv secret
// files into the default team. A later release deletes this file with
// internal/localsecrets/legacy_migrate.go.

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ImportSecrets inserts rows into the default team and sets the store
// metadata key markKey to mark, in one transaction. A row whose name and
// pipeline the team already holds fails the whole call.
func (s *Store) ImportSecrets(ctx context.Context, rows []Secret, markKey, mark string, now time.Time) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	ts := now.UTC().Unix()
	for _, sec := range rows {
		if sec.Name == "" {
			return errors.New("secrets: name required")
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO secrets (team, name, value, principal, masked, pipeline, shared, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(DefaultTeam), sec.Name, sec.Value, sec.Principal, boolInt(sec.Masked), sec.Pipeline, boolInt(sec.Shared), ts, ts); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)`,
		markKey, mark, now.UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

// ImportMark reads the metadata value [Store.ImportSecrets] set under key,
// reporting false when none was set.
func (s *Store) ImportMark(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.queryRow(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}
