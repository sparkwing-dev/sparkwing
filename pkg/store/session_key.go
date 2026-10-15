package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
)

// SessionKeyCipher binds the deployment session key to its settings row.
type SessionKeyCipher interface {
	SealBound(team, name, scope string, shared, masked bool, plain string) (string, error)
	OpenBound(team, name, scope string, shared, masked bool, envelope string) (string, error)
}

// SetSessionKeyCipher configures key protection before the store serves sessions.
func (s *Store) SetSessionKeyCipher(cipher SessionKeyCipher) {
	s.csrfKeyMu.Lock()
	defer s.csrfKeyMu.Unlock()
	s.csrfCipher, s.csrfKey = cipher, nil
}

func (s *Store) csrfSigningKey(ctx context.Context) ([]byte, error) {
	s.csrfKeyMu.Lock()
	defer s.csrfKeyMu.Unlock()
	if s.csrfCipher == nil {
		return nil, errors.New("sessions: key cipher is unavailable")
	}
	if len(s.csrfKey) > 0 {
		return s.csrfKey, nil
	}
	var envelope string
	var sealed int
	err := s.queryRow(ctx, `SELECT value,sealed FROM settings WHERE key=?`, metaKeySessionCSRFKey).Scan(&envelope, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		envelope, err = s.mintCSRFKey(ctx)
		sealed = 1
	}
	if err != nil {
		return nil, err
	}
	if sealed != 1 {
		return nil, errors.New("sessions: signing key is not sealed")
	}
	plain, err := s.csrfCipher.OpenBound("", metaKeySessionCSRFKey, "settings", false, true, envelope)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(plain)
	if err != nil || len(key) != sha256.Size {
		return nil, errors.New("sessions: malformed csrf key")
	}
	s.csrfKey = key
	return key, nil
}

func (s *Store) mintCSRFKey(ctx context.Context) (string, error) {
	minted := make([]byte, sha256.Size)
	if _, err := rand.Read(minted); err != nil {
		return "", err
	}
	envelope, err := s.csrfCipher.SealBound("", metaKeySessionCSRFKey, "settings", false, true, hex.EncodeToString(minted))
	if err != nil {
		return "", err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackOrLog(tx)
	result, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,sealed) VALUES (?,?,1) ON CONFLICT(key) DO NOTHING`, metaKeySessionCSRFKey, []byte(envelope))
	if err != nil {
		return "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if n > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
			return "", err
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=? AND sealed=1`, metaKeySessionCSRFKey).Scan(&envelope); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return envelope, nil
}
