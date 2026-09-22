package authwire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// CacheGrantPrefix begins every cache grant, so the cache tells a grant from
// its operator token without trying to verify every bearer as one.
const CacheGrantPrefix = "swcg1."

// CacheGrantTTL is how long a minted grant stays valid. It covers one claimed
// run's cache traffic; a runner mints a fresh grant for every claim.
const CacheGrantTTL = 6 * time.Hour

// CacheGrant is what the controller vouches for when it signs a grant: the
// team whose cache namespace the holder may read and write, the run it was
// minted for, and when it stops being accepted.
type CacheGrant struct {
	Team    string `json:"t"`
	Run     string `json:"r"`
	Expires int64  `json:"e"`
}

var cacheGrantTeam = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ErrCacheGrant is the one error a grant that does not verify returns, so a
// caller learns nothing about which check refused it.
var ErrCacheGrant = errors.New("cache grant is invalid or expired")

// safety: the operator token is also a bearer the cache accepts, so the MAC key is
// derived from it rather than being it; a leaked grant never reveals the token.
func cacheGrantKey(operatorToken string) []byte {
	sum := sha256.Sum256([]byte("sparkwing cache grant v1\x00" + operatorToken))
	return sum[:]
}

// MintCacheGrant signs a grant for team and run with the cache's operator
// token, valid until now+ttl. The controller holds that token for its own hop
// to the cache, so it can mint; a runner cannot.
func MintCacheGrant(operatorToken, team, run string, now time.Time, ttl time.Duration) (string, error) {
	if strings.TrimSpace(operatorToken) == "" {
		return "", errors.New("cache grant: no cache token to sign with")
	}
	if !cacheGrantTeam.MatchString(team) {
		return "", errors.New("cache grant: team must be a DNS-safe slug")
	}
	if run == "" || ttl <= 0 {
		return "", errors.New("cache grant: a run and a positive lifetime are required")
	}
	payload, err := json.Marshal(CacheGrant{Team: team, Run: run, Expires: now.Add(ttl).Unix()})
	if err != nil {
		return "", err
	}
	body := CacheGrantPrefix + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, cacheGrantKey(operatorToken))
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifyCacheGrant returns the grant raw carries when its signature matches
// operatorToken and it has not expired at now.
func VerifyCacheGrant(operatorToken, raw string, now time.Time) (CacheGrant, error) {
	if strings.TrimSpace(operatorToken) == "" || !strings.HasPrefix(raw, CacheGrantPrefix) {
		return CacheGrant{}, ErrCacheGrant
	}
	cut := strings.LastIndexByte(raw, '.')
	if cut <= len(CacheGrantPrefix) {
		return CacheGrant{}, ErrCacheGrant
	}
	body, sig := raw[:cut], raw[cut+1:]
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return CacheGrant{}, ErrCacheGrant
	}
	mac := hmac.New(sha256.New, cacheGrantKey(operatorToken))
	mac.Write([]byte(body))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return CacheGrant{}, ErrCacheGrant
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(body, CacheGrantPrefix))
	if err != nil {
		return CacheGrant{}, ErrCacheGrant
	}
	var g CacheGrant
	if err := json.Unmarshal(payload, &g); err != nil {
		return CacheGrant{}, ErrCacheGrant
	}
	if !cacheGrantTeam.MatchString(g.Team) || g.Run == "" || !now.Before(time.Unix(g.Expires, 0)) {
		return CacheGrant{}, ErrCacheGrant
	}
	return g, nil
}
