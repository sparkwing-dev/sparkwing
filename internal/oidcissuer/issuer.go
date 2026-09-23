// Package oidcissuer makes a controller an OpenID Connect issuer: it signs
// RS256 ID tokens for runs and serves the discovery document and key set a
// cloud provider reads to verify them.
//
//	iss, err := oidcissuer.New("https://api.sparkwing.dev", activePEM, previousPEM, 10*time.Minute)
//	token, exp, err := iss.Mint(claims, time.Now(), time.Time{})
package oidcissuer

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultTTL is how long a token lives when the operator names no lifetime.
	DefaultTTL = 10 * time.Minute
	// MaxTTL caps the lifetime, because a leaked token is a live credential
	// until it expires and nothing can revoke it.
	MaxTTL = time.Hour
	// MinTTL leaves a token time to reach the provider's exchange.
	MinTTL = time.Minute
	// MaxAudienceLen bounds the audience a caller may request.
	MaxAudienceLen = 256
	// DiscoveryPath and JWKSPath are served under the issuer URL.
	DiscoveryPath = "/.well-known/openid-configuration"
	JWKSPath      = "/.well-known/jwks.json"

	minKeyBits  = 2048
	cacheMaxAge = "public, max-age=3600"
)

// ErrInvalidClaim marks a claim value the subject cannot carry unambiguously.
var ErrInvalidClaim = errors.New("oidcissuer: claim value cannot appear in a subject")

type signingKey struct {
	kid string
	pub *rsa.PublicKey
}

// Issuer signs tokens with one active key and publishes it together with
// the previous key, so tokens signed before a rotation keep verifying.
type Issuer struct {
	issuer   string
	ttl      time.Duration
	active   *rsa.PrivateKey
	activeID string
	keys     []signingKey
}

// New returns an issuer for issuerURL signing with activePEM. previousPEM
// may be empty; when set it is published and never used to sign. ttl zero
// means DefaultTTL.
func New(issuerURL string, activePEM, previousPEM []byte, ttl time.Duration) (*Issuer, error) {
	if err := ValidateIssuerURL(issuerURL); err != nil {
		return nil, err
	}
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < MinTTL || ttl > MaxTTL {
		return nil, fmt.Errorf("oidcissuer: token lifetime %s is outside %s to %s", ttl, MinTTL, MaxTTL)
	}
	active, err := parsePrivateKey(activePEM)
	if err != nil {
		return nil, fmt.Errorf("oidcissuer: signing key: %w", err)
	}
	iss := &Issuer{issuer: issuerURL, ttl: ttl, active: active, activeID: Thumbprint(&active.PublicKey)}
	iss.keys = append(iss.keys, signingKey{kid: iss.activeID, pub: &active.PublicKey})
	if len(previousPEM) > 0 {
		prev, err := parsePublicKey(previousPEM)
		if err != nil {
			return nil, fmt.Errorf("oidcissuer: previous key: %w", err)
		}
		if kid := Thumbprint(prev); kid != iss.activeID {
			iss.keys = append(iss.keys, signingKey{kid: kid, pub: prev})
		}
	}
	return iss, nil
}

// ValidateIssuerURL accepts an https origin with no path, query, fragment
// or credentials. Providers fetch <issuer>/.well-known/openid-configuration
// and compare iss to it byte for byte, so anything past the origin breaks
// discovery. A loopback http origin is accepted for local testing.
func ValidateIssuerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("oidcissuer: issuer %q is not an absolute URL", raw)
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback(u.Hostname())) {
		return fmt.Errorf("oidcissuer: issuer %q must use https", raw)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("oidcissuer: issuer %q must be a bare origin such as https://api.example.com", raw)
	}
	return nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// URL is the issuer, the value of every token's iss claim.
func (i *Issuer) URL() string { return i.issuer }

// KeyIDs lists the published key ids, the active one first.
func (i *Issuer) KeyIDs() []string {
	out := make([]string, 0, len(i.keys))
	for _, k := range i.keys {
		out = append(out, k.kid)
	}
	return out
}

// TTL is the lifetime of a token minted without a shorter cap.
func (i *Issuer) TTL() time.Duration { return i.ttl }

// Claims are the run facts a token carries. Empty optional fields are
// left out of the token.
type Claims struct {
	Audience   string
	Team       string
	Pipeline   string
	Trigger    string
	RunnerKind string
	Ref        string
	SHA        string
	Repository string
	RunID      string
}

// Subject renders the stable sub claim:
//
//	team:<team>:pipeline:<pipeline>:trigger:<trigger>:runner:<runner_kind>:ref:<ref>
//
// It fails with ErrInvalidClaim when a value holds a separator, a wildcard
// a trust policy would match on, or whitespace, because such a value could
// forge a later segment.
func (c Claims) Subject() (string, error) {
	segs := []struct{ name, value string }{
		{"team", c.Team},
		{"pipeline", c.Pipeline},
		{"trigger", c.Trigger},
		{"runner", c.RunnerKind},
		{"ref", c.Ref},
	}
	var b strings.Builder
	for i, s := range segs {
		if s.value == "" && s.name != "ref" {
			return "", fmt.Errorf("%w: %s is empty", ErrInvalidClaim, s.name)
		}
		if strings.ContainsFunc(s.value, forbiddenInSubject) {
			return "", fmt.Errorf("%w: %s %q holds ':', '*', '?' or whitespace", ErrInvalidClaim, s.name, s.value)
		}
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(s.name)
		b.WriteByte(':')
		b.WriteString(s.value)
	}
	return b.String(), nil
}

func forbiddenInSubject(r rune) bool {
	return r == ':' || r == '*' || r == '?' || r <= ' ' || r == 0x7f
}

// ValidateAudience accepts 1 to MaxAudienceLen printable ASCII characters
// with no spaces, which covers URLs and plain identifiers alike.
func ValidateAudience(aud string) error {
	if aud == "" {
		return errors.New("audience is required")
	}
	if len(aud) > MaxAudienceLen {
		return fmt.Errorf("audience is longer than %d characters", MaxAudienceLen)
	}
	for i := 0; i < len(aud); i++ {
		if aud[i] <= ' ' || aud[i] >= 0x7f {
			return errors.New("audience holds a space, a control character or a non-ASCII byte")
		}
	}
	return nil
}

// Mint signs c as a token issued at now. The token expires after the
// issuer's lifetime, or at notAfter when that is earlier and non-zero.
func (i *Issuer) Mint(c Claims, now, notAfter time.Time) (string, time.Time, error) {
	if err := ValidateAudience(c.Audience); err != nil {
		return "", time.Time{}, err
	}
	sub, err := c.Subject()
	if err != nil {
		return "", time.Time{}, err
	}
	now = now.Truncate(time.Second)
	exp := now.Add(i.ttl)
	if !notAfter.IsZero() && notAfter.Before(exp) {
		exp = notAfter.Truncate(time.Second)
	}
	if !exp.After(now) {
		return "", time.Time{}, errors.New("oidcissuer: the token would expire before it is issued")
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", time.Time{}, err
	}
	payload := map[string]any{
		"iss":         i.issuer,
		"aud":         c.Audience,
		"sub":         sub,
		"iat":         now.Unix(),
		"nbf":         now.Unix(),
		"exp":         exp.Unix(),
		"jti":         hex.EncodeToString(jti),
		"team":        c.Team,
		"pipeline":    c.Pipeline,
		"trigger":     c.Trigger,
		"runner_kind": c.RunnerKind,
		"run_id":      c.RunID,
	}
	for name, v := range map[string]string{"ref": c.Ref, "sha": c.SHA, "repository": c.Repository} {
		if v != "" {
			payload[name] = v
		}
	}
	token, err := i.sign(payload)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, exp, nil
}

func (i *Issuer) sign(payload map[string]any) (string, error) {
	head, err := json.Marshal(map[string]string{"alg": "RS256", "kid": i.activeID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(head) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.active, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ClaimsSupported lists every claim a token may carry, for the discovery
// document.
var ClaimsSupported = []string{
	"iss", "aud", "sub", "iat", "nbf", "exp", "jti",
	"team", "pipeline", "trigger", "runner_kind", "ref", "sha", "repository", "run_id",
}

// Discovery is the OpenID provider metadata document.
type Discovery struct {
	Issuer                           string   `json:"issuer"`
	JWKSURI                          string   `json:"jwks_uri"`
	ResponseTypesSupported           []string `json:"response_types_supported"`
	SubjectTypesSupported            []string `json:"subject_types_supported"`
	IDTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported"`
	ClaimsSupported                  []string `json:"claims_supported"`
	ScopesSupported                  []string `json:"scopes_supported"`
}

// Discovery returns the metadata document for this issuer.
func (i *Issuer) Discovery() Discovery {
	return Discovery{
		Issuer:                           i.issuer,
		JWKSURI:                          i.issuer + JWKSPath,
		ResponseTypesSupported:           []string{"id_token"},
		SubjectTypesSupported:            []string{"public"},
		IDTokenSigningAlgValuesSupported: []string{"RS256"},
		ClaimsSupported:                  ClaimsSupported,
		ScopesSupported:                  []string{"openid"},
	}
}

// JWK is one published RSA public key.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKS is the published key set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWKS returns the active and previous public keys.
func (i *Issuer) JWKS() JWKS {
	out := JWKS{Keys: make([]JWK, 0, len(i.keys))}
	for _, k := range i.keys {
		n, e := jwkNumbers(k.pub)
		out.Keys = append(out.Keys, JWK{Kty: "RSA", Kid: k.kid, Use: "sig", Alg: "RS256", N: n, E: e})
	}
	return out
}

// ServeDiscovery answers GET /.well-known/openid-configuration.
func (i *Issuer) ServeDiscovery(w http.ResponseWriter, _ *http.Request) {
	servePublic(w, i.Discovery())
}

// ServeJWKS answers GET /.well-known/jwks.json.
func (i *Issuer) ServeJWKS(w http.ResponseWriter, _ *http.Request) {
	servePublic(w, i.JWKS())
}

func servePublic(w http.ResponseWriter, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", cacheMaxAge)
	_, _ = w.Write(body)
}

func jwkNumbers(pub *rsa.PublicKey) (n, e string) {
	return base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
}

// Thumbprint is the RFC 7638 SHA-256 JWK thumbprint of pub, base64url
// encoded, which names the key the same way on every controller.
func Thumbprint(pub *rsa.PublicKey) string {
	n, e := jwkNumbers(pub)
	// safety: RFC 7638 fixes the member order and forbids whitespace, so the
	// document is written by hand rather than by a map marshal.
	canonical := `{"e":"` + e + `","kty":"RSA","n":"` + n + `"}`
	sum := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func parsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("not a PEM block")
	}
	var key any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("PEM block %q is not a private key", block.Type)
	}
	if err != nil {
		return nil, errors.New("the private key does not parse")
	}
	rk, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the private key is not RSA, which RS256 requires")
	}
	if rk.N.BitLen() < minKeyBits {
		return nil, fmt.Errorf("the RSA key is %d bits; at least %d are required", rk.N.BitLen(), minKeyBits)
	}
	return rk, nil
}

func parsePublicKey(data []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("not a PEM block")
	}
	switch block.Type {
	case "PUBLIC KEY":
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, errors.New("the public key does not parse")
		}
		rk, ok := key.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("the public key is not RSA")
		}
		return rk, nil
	case "RSA PUBLIC KEY":
		rk, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, errors.New("the public key does not parse")
		}
		return rk, nil
	}
	priv, err := parsePrivateKey(data)
	if err != nil {
		return nil, err
	}
	return &priv.PublicKey, nil
}
