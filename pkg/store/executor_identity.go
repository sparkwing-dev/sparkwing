package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	metaKeyControllerAuthorityID = "controller_authority_id"
	controllerAuthorityPrefix    = "swfa_"
	executorIdentityPrefix       = "swex_"
)

func (s *Store) ensureControllerAuthority(ctx context.Context) (string, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	authorityID, err := ensureControllerAuthorityTx(ctx, tx)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return authorityID, nil
}

func (s *Store) controllerAuthorityID(ctx context.Context) (string, error) {
	var authorityID string
	if err := s.queryRow(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyControllerAuthorityID).Scan(&authorityID); errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	if !validOpaqueIdentity(authorityID, controllerAuthorityPrefix) {
		return "", errors.New("stored controller authority identity is invalid")
	}
	return authorityID, nil
}

func controllerAuthorityIDTx(ctx context.Context, tx *storeTx) (string, error) {
	var authorityID string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyControllerAuthorityID).Scan(&authorityID); errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	if !validOpaqueIdentity(authorityID, controllerAuthorityPrefix) {
		return "", errors.New("stored controller authority identity is invalid")
	}
	return authorityID, nil
}

func ensureControllerAuthorityTx(ctx context.Context, tx *storeTx) (string, error) {
	candidate, err := newOpaqueIdentity(controllerAuthorityPrefix)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
	 ON CONFLICT (key) DO NOTHING`, metaKeyControllerAuthorityID, candidate, time.Now().UnixNano()); err != nil {
		return "", err
	}
	var existing string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyControllerAuthorityID).Scan(&existing); err != nil {
		return "", err
	}
	if !validOpaqueIdentity(existing, controllerAuthorityPrefix) {
		return "", errors.New("stored controller authority identity is invalid")
	}
	return existing, nil
}

func newOpaqueIdentity(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}

func validOpaqueIdentity(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+32 {
		return false
	}
	encoded := value[len(prefix):]
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == 16 && encoded == strings.ToLower(encoded)
}

func executorMembershipID(authorityID, executorID string) string {
	digest := sha256.Sum256([]byte(authorityID + "\x00" + executorID))
	return fmt.Sprintf("membership-%x", digest[:12])
}

func backfillExecutorIDsTx(ctx context.Context, tx *storeTx) error {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM executors WHERE executor_id = '' ORDER BY name`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		names = append(names, name)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		executorID, err := newOpaqueIdentity(executorIdentityPrefix)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE executors SET executor_id = ? WHERE name = ? AND executor_id = ''`, executorID, name); err != nil {
			return err
		}
	}
	return nil
}
