// Package oidcissuer makes a controller an OpenID Connect issuer: it signs
// RS256 ID tokens for runs and serves the discovery document and key set a
// cloud provider reads to verify them.
//
//	iss, err := oidcissuer.New("https://api.sparkwing.dev", activePEM, publishedPEM, 10*time.Minute)
//	token, err := iss.Mint(claims, time.Now(), time.Time{})
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
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/originguard"
)

const (
	// DefaultTTL is how long a token lives when the operator names no lifetime.
	DefaultTTL = 10 * time.Minute
	// maxTTL caps the lifetime, because a leaked token is a live credential
	// until it expires and nothing can revoke it.
	maxTTL = time.Hour
	// minTTL leaves a token time to reach the provider's exchange.
	minTTL = time.Minute
	// maxAudienceLen bounds the audience a caller may request.
	maxAudienceLen = 256
	// DiscoveryPath and JWKSPath are served under the issuer URL.
	DiscoveryPath = "/.well-known/openid-configuration"
	JWKSPath      = "/.well-known/jwks.json"

	minKeyBits  = 2048
	cacheMaxAge = "public, max-age=3600"
)

// errInvalidClaim marks a claim value the subject cannot carry unambiguously.
var errInvalidClaim = errors.New("oidcissuer: claim value cannot appear in a subject")

type signingKey struct {
	kid string
	pub *rsa.PublicKey
}

// Issuer signs tokens with one active key and publishes it together with an
// optional second key: the next key before a rotation, so cached key sets
// already hold it when signing switches, or the previous key after, so
// tokens it signed keep verifying.
type Issuer struct {
	issuer   string
	ttl      time.Duration
	active   *rsa.PrivateKey
	activeID string
	keys     []signingKey
}

// New returns an issuer for issuerURL signing with activePEM. publishedPEM
// may be empty; when set it is published and never used to sign. ttl zero
// means DefaultTTL.
func New(issuerURL string, activePEM, publishedPEM []byte, ttl time.Duration) (*Issuer, error) {
	if err := validateIssuerURL(issuerURL); err != nil {
		return nil, err
	}
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if err := ValidateTTL(ttl); err != nil {
		return nil, err
	}
	active, err := parsePrivateKey(activePEM)
	if err != nil {
		return nil, fmt.Errorf("oidcissuer: signing key: %w", err)
	}
	iss := &Issuer{issuer: issuerURL, ttl: ttl, active: active, activeID: Thumbprint(&active.PublicKey)}
	iss.keys = append(iss.keys, signingKey{kid: iss.activeID, pub: &active.PublicKey})
	if len(publishedPEM) > 0 {
		prev, err := parsePublicKey(publishedPEM)
		if err != nil {
			return nil, fmt.Errorf("oidcissuer: published key: %w", err)
		}
		if kid := Thumbprint(prev); kid != iss.activeID {
			iss.keys = append(iss.keys, signingKey{kid: kid, pub: prev})
		}
	}
	return iss, nil
}

// ValidateTTL checks a token lifetime. Zero selects DefaultTTL.
func ValidateTTL(ttl time.Duration) error {
	if ttl != 0 && (ttl < minTTL || ttl > maxTTL) {
		return fmt.Errorf("oidcissuer: token lifetime %s is outside %s to %s", ttl, minTTL, maxTTL)
	}
	return nil
}

// validateIssuerURL accepts an https origin with no path, query, fragment
// or credentials. Providers fetch <issuer>/.well-known/openid-configuration
// and compare iss to it byte for byte, so anything past the origin breaks
// discovery. A loopback http origin is accepted for local testing.
func validateIssuerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("oidcissuer: issuer %q is not an absolute URL", raw)
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !originguard.LoopbackHost(u.Host)) {
		return fmt.Errorf("oidcissuer: issuer %q must use https", raw)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("oidcissuer: issuer %q must be a bare origin such as https://api.example.com", raw)
	}
	return nil
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

// runnerEnvironment spells the runner_environment claim, like GitHub's: fresh
// for a host no job of another run touched (a launcher pod or a Cloud pod,
// whose warm node serves only its own run, or a GitHub-hosted Actions runner),
// persistent for any other host, where another run may have left something
// behind.
func runnerEnvironment(fresh bool) string {
	if fresh {
		return "fresh"
	}
	return "persistent"
}

// Claims are the run facts a token carries. Empty optional fields are
// left out of the token.
type Claims struct {
	Audience   string
	Team       string
	Pipeline   string
	Trigger    string
	RunnerKind string
	// FreshHost is a host no job of another run touched.
	FreshHost  bool
	Ref        string
	SHA        string
	Repository string
	// RepositoryID is GitHub's numeric repository id, zero when the run
	// proved no repository. It survives renames and transfers, which the
	// repository name does not.
	RepositoryID int64
	RunID        string
}

// Subject renders the stable sub claim:
//
//	team:<team>:repository_id:<id>:pipeline:<pipeline>:trigger:<trigger>:runner:<runner_kind>:runner_environment:<runner_environment>:ref:<ref>
//
// The repository sits between team and pipeline because a team holds
// repositories and a repository declares pipelines, so a trust condition
// ending in "*" narrows from coarse to fine, and a pipeline name shared by
// two proven repositories never yields one subject; asserted runs carry no
// repository_id and can share one. It fails with errInvalidClaim when a value holds a separator, a wildcard
// a trust policy would match on, or whitespace, because such a value could
// forge a later segment.
func (c Claims) Subject() (string, error) {
	repoID := ""
	if c.RepositoryID > 0 {
		repoID = strconv.FormatInt(c.RepositoryID, 10)
	}
	segs := []struct{ name, value string }{
		{"team", c.Team},
		{"repository_id", repoID},
		{"pipeline", c.Pipeline},
		{"trigger", c.Trigger},
		{"runner", c.RunnerKind},
		{"runner_environment", runnerEnvironment(c.FreshHost)},
		{"ref", c.Ref},
	}
	var b strings.Builder
	for i, s := range segs {
		if s.value == "" && s.name != "ref" && s.name != "repository_id" {
			return "", fmt.Errorf("%w: %s is empty", errInvalidClaim, s.name)
		}
		if strings.ContainsFunc(s.value, forbiddenInSubject) {
			return "", fmt.Errorf("%w: %s %q holds ':', '*', '?', whitespace or a byte outside printable ASCII", errInvalidClaim, s.name, s.value)
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
	return r == ':' || r == '*' || r == '?' || r <= ' ' || r >= 0x7f
}

// ValidateAudience accepts 1 to maxAudienceLen printable ASCII characters
// with no spaces, which covers URLs and plain identifiers alike.
func ValidateAudience(aud string) error {
	if aud == "" {
		return errors.New("audience is required")
	}
	if len(aud) > maxAudienceLen {
		return fmt.Errorf("audience is longer than %d characters", maxAudienceLen)
	}
	for i := 0; i < len(aud); i++ {
		if aud[i] <= ' ' || aud[i] >= 0x7f {
			return errors.New("audience holds a space, a control character or a non-ASCII byte")
		}
	}
	return nil
}

// Token is one signed ID token with the claims that identify it.
type Token struct {
	Value     string
	ID        string
	Subject   string
	ExpiresAt time.Time
}

// Mint signs c as a token issued at now. The token expires after the
// issuer's lifetime, or at notAfter when that is earlier and non-zero.
func (i *Issuer) Mint(c Claims, now, notAfter time.Time) (Token, error) {
	if err := ValidateAudience(c.Audience); err != nil {
		return Token{}, err
	}
	sub, err := c.Subject()
	if err != nil {
		return Token{}, err
	}
	now = now.Truncate(time.Second)
	exp := now.Add(i.ttl)
	if !notAfter.IsZero() && notAfter.Before(exp) {
		exp = notAfter.Truncate(time.Second)
	}
	if !exp.After(now) {
		return Token{}, errors.New("oidcissuer: the token would expire before it is issued")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, err
	}
	jti := hex.EncodeToString(raw)
	payload := map[string]any{
		"iss":                i.issuer,
		"aud":                c.Audience,
		"sub":                sub,
		"iat":                now.Unix(),
		"nbf":                now.Unix(),
		"exp":                exp.Unix(),
		"jti":                jti,
		"team":               c.Team,
		"pipeline":           c.Pipeline,
		"trigger":            c.Trigger,
		"runner_kind":        c.RunnerKind,
		"run_id":             c.RunID,
		"runner_environment": runnerEnvironment(c.FreshHost),
	}
	if c.RepositoryID > 0 {
		payload["repository_id"] = strconv.FormatInt(c.RepositoryID, 10)
	}
	for name, v := range map[string]string{"ref": c.Ref, "sha": c.SHA, "repository": c.Repository} {
		if v != "" {
			payload[name] = v
		}
	}
	token, err := i.sign(payload)
	if err != nil {
		return Token{}, err
	}
	return Token{Value: token, ID: jti, Subject: sub, ExpiresAt: exp}, nil
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

// claimsSupported lists every claim a token may carry, for the discovery
// document.
var claimsSupported = []string{
	"iss", "aud", "sub", "iat", "nbf", "exp", "jti",
	"team", "pipeline", "trigger", "runner_kind", "runner_environment", "ref", "sha", "repository", "repository_id", "run_id",
}

// discovery is the OpenID provider metadata document.
type discovery struct {
	Issuer                           string   `json:"issuer"`
	JWKSURI                          string   `json:"jwks_uri"`
	ResponseTypesSupported           []string `json:"response_types_supported"`
	SubjectTypesSupported            []string `json:"subject_types_supported"`
	IDTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported"`
	ClaimsSupported                  []string `json:"claims_supported"`
	ScopesSupported                  []string `json:"scopes_supported"`
}

// discovery returns the metadata document for this issuer.
func (i *Issuer) discovery() discovery {
	return discovery{
		Issuer:                           i.issuer,
		JWKSURI:                          i.issuer + JWKSPath,
		ResponseTypesSupported:           []string{"id_token"},
		SubjectTypesSupported:            []string{"public"},
		IDTokenSigningAlgValuesSupported: []string{"RS256"},
		ClaimsSupported:                  claimsSupported,
		ScopesSupported:                  []string{"openid"},
	}
}

// jwk is one published RSA public key.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKS is the published key set.
type keySet struct {
	Keys []jwk `json:"keys"`
}

// jwks returns the active public key, then the published one.
func (i *Issuer) jwks() keySet {
	out := keySet{Keys: make([]jwk, 0, len(i.keys))}
	for _, k := range i.keys {
		n, e := jwkNumbers(k.pub)
		out.Keys = append(out.Keys, jwk{Kty: "RSA", Kid: k.kid, Use: "sig", Alg: "RS256", N: n, E: e})
	}
	return out
}

// ServeDiscovery answers GET /.well-known/openid-configuration.
func (i *Issuer) ServeDiscovery(w http.ResponseWriter, _ *http.Request) {
	servePublic(w, i.discovery())
}

// ServeJWKS answers GET /.well-known/jwks.json.
func (i *Issuer) ServeJWKS(w http.ResponseWriter, _ *http.Request) {
	servePublic(w, i.jwks())
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

// parsePublicKey reads the published key, which must be public: the file is
// not held to the signing key's permissions, so it may not carry one.
func parsePublicKey(data []byte) (*rsa.PublicKey, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, errors.New("not a PEM block")
	}
	if extra, _ := pem.Decode(rest); extra != nil {
		return nil, errors.New("holds more than one PEM block; publish only the public key")
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
	return nil, errors.New("is not a public key PEM; publish only the public key (openssl pkey -pubout)")
}
