// Package googleauth runs the server half of Google sign-in: it builds the
// authorization URL for a PKCE flow, redeems the code the browser brings back,
// and verifies the ID token Google returns against Google's published signing
// keys before trusting anything it says.
package googleauth

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
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Google's published endpoints. Config carries them as fields so a test can
// point a client at a local issuer.
const (
	GoogleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	GoogleTokenURL = "https://oauth2.googleapis.com/token"
	GoogleJWKSURL  = "https://www.googleapis.com/oauth2/v3/certs"
)

var googleIssuers = []string{"https://accounts.google.com", "accounts.google.com"}

const (
	// safety: a provider that stopped answering fails the sign-in instead of holding the request.
	providerTimeout = 10 * time.Second
	maxBody         = 1 << 20
	clockSkew       = time.Minute
	defaultKeyTTL   = time.Hour
	// safety: forged key ids refetch Google's keys at most this often, so they cannot make every request fetch.
	minRefetch = time.Minute
)

// Errors a sign-in can fail with. ErrRejected covers every answer that means
// the presented code or token does not prove who the caller says they are.
var (
	ErrRejected   = errors.New("googleauth: sign-in rejected")
	ErrUnverified = errors.New("googleauth: google has not verified this email address")
)

// Config names the OAuth client and where Google answers.
type Config struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	JWKSURL      string
	Issuers      []string
	HTTP         *http.Client
	Now          func() time.Time
}

// Google returns the configuration for Google's production endpoints.
func Google(clientID, clientSecret string) Config {
	return Config{
		ClientID: clientID, ClientSecret: clientSecret,
		AuthURL: GoogleAuthURL, TokenURL: GoogleTokenURL, JWKSURL: GoogleJWKSURL,
		Issuers: googleIssuers,
	}
}

// Claims is what a verified ID token asserts about the person who signed in.
// EmailVerified is always true on a value Exchange returns, because Exchange
// refuses an address Google has not verified.
type Claims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	GivenName     string
	HostedDomain  string
}

// Client talks to one OAuth client registration.
type Client struct {
	cfg  Config
	keys keyCache
}

// New returns a client for cfg.
func New(cfg Config) *Client {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: providerTimeout}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Client{cfg: cfg}
}

// CodeChallenge is the S256 PKCE challenge for a verifier.
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL is where the browser goes to sign in. The challenge ties the
// code Google issues to verifier, so only whoever holds verifier can redeem it.
func (c *Client) AuthorizeURL(state, verifier, redirectURI string) string {
	q := url.Values{
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {state},
		"code_challenge":        {CodeChallenge(verifier)},
		"code_challenge_method": {"S256"},
		// safety: without it a second sign-in silently reuses the browser's Google session,
		// signing people in as an account they did not mean to use.
		"prompt": {"select_account"},
	}
	return c.cfg.AuthURL + "?" + q.Encode()
}

// Exchange redeems code for an ID token and returns its verified claims.
func (c *Client) Exchange(ctx context.Context, code, verifier, redirectURI string) (Claims, error) {
	form := url.Values{
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
		"code":          {code},
		"code_verifier": {verifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Claims{}, fmt.Errorf("googleauth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var body struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	status, err := c.fetchJSON(req, &body)
	if err != nil {
		return Claims{}, err
	}
	if status == http.StatusBadRequest || status == http.StatusUnauthorized || body.Error != "" {
		return Claims{}, fmt.Errorf("%w: google refused the code: %s", ErrRejected, body.Error)
	}
	if status != http.StatusOK {
		return Claims{}, fmt.Errorf("googleauth: token endpoint answered %d", status)
	}
	if body.IDToken == "" {
		return Claims{}, fmt.Errorf("%w: google returned no id token", ErrRejected)
	}
	return c.Verify(ctx, body.IDToken)
}

type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type idClaims struct {
	Iss           string       `json:"iss"`
	Aud           audience     `json:"aud"`
	Azp           string       `json:"azp"`
	Sub           string       `json:"sub"`
	Exp           int64        `json:"exp"`
	Iat           int64        `json:"iat"`
	Email         string       `json:"email"`
	EmailVerified flexibleBool `json:"email_verified"`
	Name          string       `json:"name"`
	GivenName     string       `json:"given_name"`
	HD            string       `json:"hd"`
}

// Verify checks an ID token's signature, issuer, audience, expiry and email
// verification, in that order, and returns its claims.
func (c *Client) Verify(ctx context.Context, token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("%w: id token is not a JWT", ErrRejected)
	}
	var h header
	if err := decodeSegment(parts[0], &h); err != nil {
		return Claims{}, err
	}
	// safety: the algorithm is pinned rather than read from the token, so a
	// token naming "none" or an HMAC algorithm is refused before any key is used.
	if h.Alg != "RS256" {
		return Claims{}, fmt.Errorf("%w: id token algorithm %q", ErrRejected, h.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: id token signature encoding", ErrRejected)
	}
	key, err := c.key(ctx, h.Kid)
	if err != nil {
		return Claims{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return Claims{}, fmt.Errorf("%w: id token signature does not verify", ErrRejected)
	}

	var cl idClaims
	if err := decodeSegment(parts[1], &cl); err != nil {
		return Claims{}, err
	}
	now := c.cfg.Now()
	switch {
	case !slices.Contains(c.cfg.Issuers, cl.Iss):
		return Claims{}, fmt.Errorf("%w: id token issuer %q", ErrRejected, cl.Iss)
	case !slices.Contains(cl.Aud, c.cfg.ClientID):
		return Claims{}, fmt.Errorf("%w: id token was issued to another client", ErrRejected)
	case len(cl.Aud) > 1 && cl.Azp != c.cfg.ClientID:
		return Claims{}, fmt.Errorf("%w: id token authorized party is another client", ErrRejected)
	case cl.Exp == 0 || !now.Before(time.Unix(cl.Exp, 0).Add(clockSkew)):
		return Claims{}, fmt.Errorf("%w: id token expired", ErrRejected)
	case cl.Iat != 0 && time.Unix(cl.Iat, 0).After(now.Add(clockSkew)):
		return Claims{}, fmt.Errorf("%w: id token issued in the future", ErrRejected)
	case cl.Sub == "":
		return Claims{}, fmt.Errorf("%w: id token names no subject", ErrRejected)
	case cl.Email == "" || !bool(cl.EmailVerified):
		return Claims{}, ErrUnverified
	}
	return Claims{
		Subject: cl.Sub, Email: strings.ToLower(strings.TrimSpace(cl.Email)), EmailVerified: true,
		Name: cl.Name, GivenName: cl.GivenName, HostedDomain: strings.ToLower(cl.HD),
	}, nil
}

func decodeSegment(seg string, out any) error {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return fmt.Errorf("%w: id token segment encoding", ErrRejected)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: id token segment is not JSON", ErrRejected)
	}
	return nil
}

type keyCache struct {
	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	expires   time.Time
	fetchedAt time.Time
}

func (c *Client) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.keys.mu.Lock()
	defer c.keys.mu.Unlock()
	now := c.cfg.Now()
	if k, ok := c.keys.keys[kid]; ok && now.Before(c.keys.expires) {
		return k, nil
	}
	// safety: an unknown key id refetches at most once a minute, because Google
	// rotates keys rarely and a forged kid must not turn every request into a
	// fetch.
	stale := !now.Before(c.keys.expires)
	if !stale && now.Sub(c.keys.fetchedAt) < minRefetch {
		return nil, fmt.Errorf("%w: id token signed by an unknown key", ErrRejected)
	}
	keys, ttl, err := c.fetchKeys(ctx)
	if err != nil {
		return nil, err
	}
	c.keys.keys, c.keys.fetchedAt, c.keys.expires = keys, now, now.Add(ttl)
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: id token signed by an unknown key", ErrRejected)
}

func (c *Client) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.JWKSURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("googleauth: build key request: %w", err)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	status, header, err := c.fetchJSONWithHeader(req, &set)
	if err != nil {
		return nil, 0, err
	}
	if status != http.StatusOK {
		return nil, 0, fmt.Errorf("googleauth: key endpoint answered %d", status)
	}
	out := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	return out, maxAge(header.Get("Cache-Control")), nil
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

func (c *Client) fetchJSON(req *http.Request, out any) (int, error) {
	status, _, err := c.fetchJSONWithHeader(req, out)
	return status, err
}

func (c *Client) fetchJSONWithHeader(req *http.Request, out any) (int, http.Header, error) {
	ctx, cancel := context.WithTimeout(req.Context(), providerTimeout)
	defer cancel()
	resp, err := c.cfg.HTTP.Do(req.WithContext(ctx))
	if err != nil {
		return 0, nil, fmt.Errorf("googleauth: %s: %w", req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, nil, fmt.Errorf("googleauth: read %s: %w", req.URL.Host, err)
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil && resp.StatusCode == http.StatusOK {
			return 0, nil, fmt.Errorf("googleauth: unreadable answer from %s: %w", req.URL.Host, err)
		}
	}
	return resp.StatusCode, resp.Header, nil
}

// hack: a JWT may spell aud as a string or as a list.
type audience []string

func (a *audience) UnmarshalJSON(raw []byte) error {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// hack: Google has spelled email_verified both as a boolean and as the string "true".
type flexibleBool bool

func (b *flexibleBool) UnmarshalJSON(raw []byte) error {
	var asBool bool
	if err := json.Unmarshal(raw, &asBool); err == nil {
		*b = flexibleBool(asBool)
		return nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return err
	}
	parsed, err := strconv.ParseBool(asString)
	if err != nil {
		return err
	}
	*b = flexibleBool(parsed)
	return nil
}
