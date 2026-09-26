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

// ImportSecrets writes rows into the default team, each only where the team
// holds no row under that name and pipeline, then sets in the store's
// metadata every key marks returns, given the names that were already
// present and kept the value they had. Both happen in one transaction.
func (s *Store) ImportSecrets(ctx context.Context, rows []Secret, marks func(present []string) (map[string]string, error), now time.Time) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	ts := now.UTC().Unix()
	var present []string
	for _, sec := range rows {
		if sec.Name == "" {
			return errors.New("secrets: name required")
		}
		res, err := tx.ExecContext(ctx, `
            INSERT INTO secrets (team, name, value, principal, masked, pipeline, shared, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(team, name, pipeline) DO NOTHING`,
			string(DefaultTeam), sec.Name, sec.Value, sec.Principal, boolInt(sec.Masked), sec.Pipeline, boolInt(sec.Shared), ts, ts)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			present = append(present, sec.Name)
		}
	}
	values, err := marks(present)
	if err != nil {
		return err
	}
	if err := setImportMarksTx(ctx, tx, values, now); err != nil {
		return err
	}
	return tx.Commit()
}

// SetImportMarks sets every key in marks to its value in the store's
// metadata, in one transaction.
func (s *Store) SetImportMarks(ctx context.Context, marks map[string]string, now time.Time) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := setImportMarksTx(ctx, tx, marks, now); err != nil {
		return err
	}
	return tx.Commit()
}

func setImportMarksTx(ctx context.Context, tx *storeTx, marks map[string]string, now time.Time) error {
	for key, value := range marks {
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
            ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, value, now.UnixNano()); err != nil {
			return err
		}
	}
	return nil
}

// ImportMark reads the metadata value [Store.ImportSecrets] or
// [Store.SetImportMarks] set under key,
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
