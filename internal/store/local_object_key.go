package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

const metaKeyLocalObjects = "local_object_signing_key"

// LocalObjectSigningKey returns the local filesystem URL key, encrypted under the installation keyring.
// The cipher must be ready before intake can seal requester secrets inside a transaction.
func (o *Operator) LocalObjectSigningKey(ctx context.Context, cipher SessionKeyCipher) ([]byte, error) {
	if o.s.dialect != DialectSQLite || cipher == nil {
		return nil, errors.New("local object signing key is unavailable")
	}
	var envelope string
	var sealed int
	err := o.s.queryRow(ctx, `SELECT value,sealed FROM settings WHERE key=?`, metaKeyLocalObjects).Scan(&envelope, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		minted := make([]byte, sha256.Size)
		if _, err := rand.Read(minted); err != nil {
			return nil, err
		}
		envelope, err = cipher.SealBound(secrets.PurposeSetting, "", metaKeyLocalObjects, "settings", false, true, hex.EncodeToString(minted))
		if errors.Is(err, secrets.ErrKeyRefused) {
			return nil, &apierr.Error{Code: apierr.BadRequest, Message: "local object signing key encryption failed", Hint: err.Error()}
		}
		if err != nil || !secrets.IsBound(envelope) {
			return nil, errors.New("local object signing key encryption failed")
		}
		err = o.s.inTx(ctx, func(tx *storeTx) error {
			if _, err := tx.execContext(ctx, `INSERT INTO settings(key,value,sealed) VALUES (?,?,1) ON CONFLICT(key) DO NOTHING`, metaKeyLocalObjects, []byte(envelope)); err != nil {
				return err
			}
			return tx.QueryRowContext(ctx, `SELECT value,sealed FROM settings WHERE key=?`, metaKeyLocalObjects).Scan(&envelope, &sealed)
		})
	}
	if err != nil {
		return nil, err
	}
	if sealed != 1 {
		return nil, errors.New("local object signing key is not sealed")
	}
	plain, err := cipher.OpenBound(secrets.PurposeSetting, "", metaKeyLocalObjects, "settings", false, true, envelope)
	if err != nil {
		return nil, &apierr.Error{Code: apierr.BadRequest, Message: "local object signing key cannot be opened", Hint: err.Error()}
	}
	key, err := hex.DecodeString(plain)
	if err != nil || len(key) != sha256.Size {
		return nil, errors.New("local object signing key is malformed")
	}
	return key, nil
}
