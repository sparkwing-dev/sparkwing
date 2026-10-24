package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// SessionKeyCipher binds the deployment session key to its settings row.
type SessionKeyCipher interface {
	SealBound(purpose secrets.Purpose, team, name, scope string, shared, masked bool, plain string) (string, error)
	OpenBound(purpose secrets.Purpose, team, name, scope string, shared, masked bool, envelope string) (string, error)
}

func (o *Operator) csrfSigningKey(ctx context.Context) ([]byte, error) {
	o.s.csrfKeyMu.Lock()
	defer o.s.csrfKeyMu.Unlock()
	if o.s.csrfCipher == nil {
		return nil, errors.New("sessions: key cipher is unavailable")
	}
	if len(o.s.csrfKey) > 0 {
		return o.s.csrfKey, nil
	}
	var envelope string
	var sealed int
	err := o.s.queryRow(ctx, `SELECT value,sealed FROM settings WHERE key=?`, metaKeySessionCSRFKey).Scan(&envelope, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		envelope, err = o.mintCSRFKey(ctx)
		sealed = 1
	}
	if err != nil {
		return nil, err
	}
	if sealed != 1 {
		return nil, errors.New("sessions: signing key is not sealed")
	}
	plain, err := o.s.csrfCipher.OpenBound(secrets.PurposeSetting, "", metaKeySessionCSRFKey, "settings", false, true, envelope)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(plain)
	if err != nil || len(key) != sha256.Size {
		return nil, errors.New("sessions: malformed csrf key")
	}
	o.s.csrfKey = key
	return key, nil
}

func (o *Operator) mintCSRFKey(ctx context.Context) (string, error) {
	minted := make([]byte, sha256.Size)
	if _, err := rand.Read(minted); err != nil {
		return "", err
	}
	envelope, err := o.s.csrfCipher.SealBound(secrets.PurposeSetting, "", metaKeySessionCSRFKey, "settings", false, true, hex.EncodeToString(minted))
	if err != nil {
		return "", err
	}
	return inTxResult(ctx, o.s, func(tx *storeTx) (string, error) {
		result, err := tx.execContext(ctx, `INSERT INTO settings(key,value,sealed) VALUES (?,?,1) ON CONFLICT(key) DO NOTHING`, metaKeySessionCSRFKey, []byte(envelope))
		if err != nil {
			return "", err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return "", err
		}
		if n > 0 {
			if _, err := tx.execContext(ctx, `DELETE FROM sessions`); err != nil {
				return "", err
			}
		}
		if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=? AND sealed=1`, metaKeySessionCSRFKey).Scan(&envelope); err != nil {
			return "", err
		}
		return envelope, nil
	})
}
