package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

type oidcAuditKey struct {
	id  string
	key []byte
}

func newOIDCAuditKey(key []byte) oidcAuditKey {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("sparkwing oidc audit key v1"))
	derived := mac.Sum(nil)
	id := sha256.Sum256(derived)
	return oidcAuditKey{id: hex.EncodeToString(id[:]), key: derived}
}

// OIDCAuditDigest computes a field-separated HMAC using the current secrets key,
// or the retained key named by keyID. An empty keyID selects the current key.
// Only audience and subject are accepted. The returned ID belongs with the digest.
func (c *Cipher) OIDCAuditDigest(keyID, field, value string) (string, string, error) {
	if c == nil || (field != "audience" && field != "subject") {
		return "", "", errors.New("OIDC audit digest requires a key and an audience or subject field")
	}
	for _, key := range c.oidcAuditKeys {
		if keyID != "" && keyID != key.id {
			continue
		}
		mac := hmac.New(sha256.New, key.key)
		mac.Write([]byte("sparkwing oidc audit v1\x00" + field + "\x00" + value))
		return key.id, hex.EncodeToString(mac.Sum(nil)), nil
	}
	return "", "", errors.New("OIDC audit digest key is unavailable; retain the named retired secrets key for investigation")
}
