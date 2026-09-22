// Package jwks verifies RS256-signed JSON Web Tokens against a key set an
// identity provider publishes, fetching and caching the keys. Callers check
// the claims their provider defines; this package proves only that the
// provider signed the token.
//
//	keys := jwks.New("https://token.actions.githubusercontent.com/.well-known/jwks", nil, nil)
//	var claims struct{ Iss string `json:"iss"` }
//	if err := keys.Verify(ctx, token, &claims); err != nil { ... }
package jwks

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// safety: a provider that stopped answering fails the verification instead of holding the request.
	fetchTimeout  = 10 * time.Second
	maxBody       = 1 << 20
	defaultKeyTTL = time.Hour
	// safety: forged key ids refetch the provider's keys at most this often, so they cannot make every request fetch.
	minRefetch = time.Minute
)

// ErrRejected covers every token that does not carry a valid signature from
// the key set: a malformed token, a pinned-out algorithm, an unknown key, or
// a signature that does not verify.
var ErrRejected = errors.New("jwks: token rejected")

// KeySet is one provider's published keys. It is safe for concurrent use.
type KeySet struct {
	url  string
	http *http.Client
	now  func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	expires   time.Time
	fetchedAt time.Time
}

// New returns a key set read from url. A nil client gets a ten-second
// timeout and a nil clock reads time.Now.
func New(url string, client *http.Client, now func() time.Time) *KeySet {
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	if now == nil {
		now = time.Now
	}
	return &KeySet{url: url, http: client, now: now}
}

type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// Verify checks token's RS256 signature against the key set and decodes its
// payload into claims. It checks no claim.
func (k *KeySet) Verify(ctx context.Context, token string, claims any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("%w: not a JWT", ErrRejected)
	}
	var h header
	if err := DecodeSegment(parts[0], &h); err != nil {
		return err
	}
	// safety: the algorithm is pinned rather than read from the token, so a
	// token naming "none" or an HMAC algorithm is refused before any key is used.
	if h.Alg != "RS256" {
		return fmt.Errorf("%w: algorithm %q", ErrRejected, h.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("%w: signature encoding", ErrRejected)
	}
	key, err := k.key(ctx, h.Kid)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return fmt.Errorf("%w: signature does not verify", ErrRejected)
	}
	return DecodeSegment(parts[1], claims)
}

// DecodeSegment decodes one base64url JWT segment as JSON into out.
func DecodeSegment(seg string, out any) error {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return fmt.Errorf("%w: segment encoding", ErrRejected)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: segment is not JSON", ErrRejected)
	}
	return nil
}

func (k *KeySet) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	if key, ok := k.keys[kid]; ok && now.Before(k.expires) {
		return key, nil
	}
	// safety: an unknown key id refetches at most once a minute, because
	// providers rotate keys rarely and a forged kid must not turn every
	// request into a fetch.
	stale := !now.Before(k.expires)
	if !stale && now.Sub(k.fetchedAt) < minRefetch {
		return nil, fmt.Errorf("%w: signed by an unknown key", ErrRejected)
	}
	keys, ttl, err := k.fetch(ctx)
	if err != nil {
		return nil, err
	}
	k.keys, k.fetchedAt, k.expires = keys, now, now.Add(ttl)
	if key, ok := keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("%w: signed by an unknown key", ErrRejected)
}

func (k *KeySet) fetch(ctx context.Context) (map[string]*rsa.PublicKey, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("jwks: build key request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("jwks: %s: %w", req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, 0, fmt.Errorf("jwks: read %s: %w", req.URL.Host, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("jwks: key endpoint %s answered %d", req.URL.Host, resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, 0, fmt.Errorf("jwks: unreadable key set from %s: %w", req.URL.Host, err)
	}
	out := map[string]*rsa.PublicKey{}
	for _, key := range set.Keys {
		if key.Kty != "RSA" || key.Kid == "" {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(key.N)
		e, errE := base64.RawURLEncoding.DecodeString(key.E)
		if errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		out[key.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	return out, maxAge(resp.Header.Get("Cache-Control")), nil
}

func maxAge(cacheControl string) time.Duration {
	for _, directive := range strings.Split(cacheControl, ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(directive), "=")
		if !ok || !strings.EqualFold(name, "max-age") {
			continue
		}
		if secs, err := strconv.Atoi(value); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return defaultKeyTTL
}

// Audience is a JWT aud claim, which the spec lets a token spell as one
// string or as a list.
type Audience []string

// UnmarshalJSON accepts both spellings.
func (a *Audience) UnmarshalJSON(raw []byte) error {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		*a = Audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return err
	}
	*a = many
	return nil
}
