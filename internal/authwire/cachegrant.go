package authwire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// CacheGrantPrefix begins every cache grant, so the cache tells a grant from
// its operator token without trying to verify every bearer as one.
const CacheGrantPrefix = "swcg1."

// CacheGrantTTL bounds a grant a runner token mints through its live node or
// trigger claim fence. It covers one claimed run's cache traffic; a runner
// mints a fresh grant for every claim.
const CacheGrantTTL = 6 * time.Hour

// CacheGrant is what the controller vouches for when it signs a grant: the
// team whose cache namespace the holder may read and write, the run and live
// claim it was minted for, the repository and refs that confine it within the
// team, and when it stops being accepted.
type CacheGrant struct {
	Team    string      `json:"t"`
	Run     string      `json:"r"`
	Expires int64       `json:"e"`
	Claim   *CacheClaim `json:"c,omitempty"`
	Scope   *CacheScope `json:"s,omitempty"`
}

// CacheScope is the repository and git refs the controller read from the
// grant's run. Refs[0] is the run's own ref, the only one the grant writes
// under; the rest are the pull request's base and the default branch, which
// it also reads, in that order.
type CacheScope struct {
	Repo string   `json:"p"`
	Refs []string `json:"f"`
}

// ScopePrefixes returns the key prefixes, within the grant's team namespace,
// that the grant reads in order. It writes only under the first. It reads the
// unscoped prefix last, so entries written before grants carried a scope stay
// readable but are never written again. A grant without a scope never
// verifies, and has no prefixes.
func (g CacheGrant) ScopePrefixes() []string {
	if g.Scope == nil {
		return nil
	}
	refs := g.Scope.Refs
	if len(refs) == 0 {
		refs = []string{""}
	}
	var out []string
	for _, ref := range refs {
		// safety: a hash keeps any repository name or ref a single safe path
		// segment, and the NUL keeps two different pairs from joining alike.
		sum := sha256.Sum256([]byte(g.Scope.Repo + "\x00" + ref))
		if p := "scopes/" + hex.EncodeToString(sum[:16]) + "/"; !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return append(out, "")
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

// CacheClaimToken is the [CacheClaim] kind of a claim token's grant, whose
// Generation and NodeID name the claim the token was minted for.
const CacheClaimToken = "claim"

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

// MintClaimCacheGrant signs a grant for team and run with signingKey, the key
// named by [CacheGrantKeyEnv], valid until now+ttl, bound to the live claim
// that asked for it and the repository and refs of its run. The controller and
// the cache hold that key; a runner does not, so it cannot mint.
func MintClaimCacheGrant(signingKey, team, run string, now time.Time, ttl time.Duration, claim *CacheClaim, scope *CacheScope) (string, error) {
	if strings.TrimSpace(signingKey) == "" {
		return "", errors.New("cache grant: no grant key to sign with")
	}
	// safety: the cache honors a grant on its signature alone, so one with no claim or scope would open
	// the team's whole tree to whoever held it until it expired.
	if claim == nil || scope == nil {
		return "", errors.New("cache grant: a live claim and the run's repository scope are required")
	}
	if !cacheGrantTeam.MatchString(team) {
		return "", errors.New("cache grant: team must be a DNS-safe slug")
	}
	if run == "" || ttl <= 0 {
		return "", errors.New("cache grant: a run and a positive lifetime are required")
	}
	payload, err := json.Marshal(CacheGrant{Team: team, Run: run, Expires: now.Add(ttl).Unix(), Claim: claim, Scope: scope})
	if err != nil {
		return "", err
	}
	body := CacheGrantPrefix + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, cacheGrantKey(signingKey))
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifyCacheGrant returns the grant raw carries when its signature matches
// signingKey, it names the claim and scope it was minted for, and it has not
// expired at now.
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
	if !cacheGrantTeam.MatchString(g.Team) || g.Run == "" || g.Claim == nil || g.Scope == nil ||
		!now.Before(time.Unix(g.Expires, 0)) {
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
