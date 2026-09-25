package authwire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"
)

// signCacheGrant MACs an arbitrary payload the way MintCacheGrant does, so a
// test can hand VerifyCacheGrant a grant Mint would refuse to produce.
func signCacheGrant(signingKey, prefix, payload string) string {
	body := prefix + base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, cacheGrantKey(signingKey))
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestVerifyCacheGrantRefusesSignedButInvalidGrants(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	valid := `{"t":"team-a","r":"run-1","e":1800003600}`
	if _, err := VerifyCacheGrant("k", signCacheGrant("k", CacheGrantPrefix, valid), now); err != nil {
		t.Fatalf("control grant refused: %v", err)
	}
	cases := map[string]string{
		// A cache started without a key must not accept grants MACed with
		// the key derived from the empty string.
		"empty key":     signCacheGrant("", CacheGrantPrefix, valid),
		"no prefix":     signCacheGrant("k", "", valid),
		"escaping team": signCacheGrant("k", CacheGrantPrefix, `{"t":"../team-b","r":"run-1","e":1800003600}`),
		"no run":        signCacheGrant("k", CacheGrantPrefix, `{"t":"team-a","r":"","e":1800003600}`),
	}
	for name, raw := range cases {
		key := "k"
		if name == "empty key" {
			key = ""
		}
		if g, err := VerifyCacheGrant(key, raw, now); err == nil {
			t.Errorf("%s: verified %+v", name, g)
		}
	}
}
