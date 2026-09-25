package authwire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
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
	Team    string      `json:"t"`
	Run     string      `json:"r"`
	Expires int64       `json:"e"`
	Claim   *CacheClaim `json:"c,omitempty"`
}

// CacheClaim binds a signed download to the claim that requested its grant.
type CacheClaim struct {
	Kind          string `json:"k"`
	NodeID        string `json:"n,omitempty"`
	HolderID      string `json:"h,omitempty"`
	MembershipID  string `json:"m,omitempty"`
	ReservationID string `json:"r,omitempty"`
	Generation    int64  `json:"g"`
	Principal     string `json:"p"`
	TokenPrefix   string `json:"t"`
}

// OperatorTeam is the team the deployment operator's own runs belong to. The
// cache lets its grants read every mirror, because only the operator
// registers mirrors and they are the operator's own repositories; the store
// reserves the slug, so no other team can take it.
const OperatorTeam = "default"

var cacheGrantTeam = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ErrCacheGrant is the one error a grant that does not verify returns, so a
// caller learns nothing about which check refused it.
var ErrCacheGrant = errors.New("cache grant is invalid or expired")

// safety: the MAC key is derived from the signing key under a fixed label, so
// the key is never used raw and a leaked grant reveals nothing about it.
func cacheGrantKey(signingKey string) []byte {
	sum := sha256.Sum256([]byte("sparkwing cache grant v1\x00" + signingKey))
	return sum[:]
}

// CacheGrantKeyEnv names the variable the controller and the cache read the
// grant signing key from. The key is a secret of its own: never the cache's
// operator token and never a runner's token, because pipeline code can read
// a runner's token and whoever holds the key can open any team's cache tree.
const CacheGrantKeyEnv = "SPARKWING_CACHE_GRANT_KEY"

// MintCacheGrant signs a grant for team and run with signingKey, the key
// named by [CacheGrantKeyEnv], valid until now+ttl. The controller and the
// cache hold that key; a runner does not, so it cannot mint.
func MintCacheGrant(signingKey, team, run string, now time.Time, ttl time.Duration) (string, error) {
	return MintClaimCacheGrant(signingKey, team, run, now, ttl, nil)
}

// MintClaimCacheGrant signs a cache grant with its issuing live claim.
func MintClaimCacheGrant(signingKey, team, run string, now time.Time, ttl time.Duration, claim *CacheClaim) (string, error) {
	if strings.TrimSpace(signingKey) == "" {
		return "", errors.New("cache grant: no grant key to sign with")
	}
	if !cacheGrantTeam.MatchString(team) {
		return "", errors.New("cache grant: team must be a DNS-safe slug")
	}
	if run == "" || ttl <= 0 {
		return "", errors.New("cache grant: a run and a positive lifetime are required")
	}
	payload, err := json.Marshal(CacheGrant{Team: team, Run: run, Expires: now.Add(ttl).Unix(), Claim: claim})
	if err != nil {
		return "", err
	}
	body := CacheGrantPrefix + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, cacheGrantKey(signingKey))
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifyCacheGrant returns the grant raw carries when its signature matches
// signingKey and it has not expired at now.
func VerifyCacheGrant(signingKey, raw string, now time.Time) (CacheGrant, error) {
	if strings.TrimSpace(signingKey) == "" || !strings.HasPrefix(raw, CacheGrantPrefix) {
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
	mac := hmac.New(sha256.New, cacheGrantKey(signingKey))
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

// CacheGrantEnv names the variable a runner hands a run's cache grant in. The
// run's processes send it to the cache in place of the operator token, which a
// runner never holds.
const CacheGrantEnv = "SPARKWING_CACHE_GRANT"

// CacheTokenEnv names the variable an operator's own shell carries the cache's
// operator token in.
const CacheTokenEnv = "SPARKWING_CACHE_TOKEN"

// CacheBearerFromEnv returns the bearer this process sends the cache: the
// run's grant when a runner handed it one, otherwise the operator token.
func CacheBearerFromEnv() string {
	if grant := os.Getenv(CacheGrantEnv); grant != "" {
		return grant
	}
	return os.Getenv(CacheTokenEnv)
}
