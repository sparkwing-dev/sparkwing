package authwire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"
)

func signCacheGrant(signingKey, prefix, payload string) string {
	body := prefix + base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, cacheGrantKey(signingKey))
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestVerifyCacheGrantRefusesSignedButInvalidGrants(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	valid := `{"t":"team-a","r":"run-1","e":1800003600,"c":{"k":"claim","g":1,"p":"agent:a","t":"swc_abc"},"s":{"p":"github.com/acme/app","f":["refs/heads/main"]}}`
	if _, err := VerifyCacheGrant("k", signCacheGrant("k", CacheGrantPrefix, valid), now); err != nil {
		t.Fatalf("control grant refused: %v", err)
	}
	cases := map[string]string{
		"empty key":     signCacheGrant("", CacheGrantPrefix, valid),
		"no prefix":     signCacheGrant("k", "", valid),
		"escaping team": signCacheGrant("k", CacheGrantPrefix, `{"t":"../team-b","r":"run-1","e":1800003600}`),
		"no run": signCacheGrant("k", CacheGrantPrefix,
			`{"t":"team-a","r":"","e":1800003600,"c":{"k":"claim","g":1,"p":"agent:a","t":"swc_abc"},"s":{"p":"github.com/acme/app","f":["refs/heads/main"]}}`),
		"whole team, no claim or scope": signCacheGrant("k", CacheGrantPrefix, `{"t":"team-a","r":"run-1","e":1800003600}`),
		"no claim": signCacheGrant("k", CacheGrantPrefix,
			`{"t":"team-a","r":"run-1","e":1800003600,"s":{"p":"github.com/acme/app","f":["refs/heads/main"]}}`),
		"no scope": signCacheGrant("k", CacheGrantPrefix,
			`{"t":"team-a","r":"run-1","e":1800003600,"c":{"k":"claim","g":1,"p":"agent:a","t":"swc_abc"}}`),
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
