// Package googleauth runs the server half of Google sign-in: it builds the
// authorization URL for a PKCE flow, redeems the code the browser brings back,
// and verifies the ID token Google returns against Google's published signing
// keys before trusting anything it says.
package googleauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/jwks"
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
	keys *jwks.KeySet
}

// New returns a client for cfg.
func New(cfg Config) *Client {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: providerTimeout}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Client{cfg: cfg, keys: jwks.New(cfg.JWKSURL, cfg.HTTP, cfg.Now)}
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

type idClaims struct {
	Iss           string        `json:"iss"`
	Aud           jwks.Audience `json:"aud"`
	Azp           string        `json:"azp"`
	Sub           string        `json:"sub"`
	Exp           int64         `json:"exp"`
	Iat           int64         `json:"iat"`
	Email         string        `json:"email"`
	EmailVerified flexibleBool  `json:"email_verified"`
	Name          string        `json:"name"`
	GivenName     string        `json:"given_name"`
	HD            string        `json:"hd"`
}

// Verify checks an ID token's signature, issuer, audience, expiry and email
// verification, in that order, and returns its claims.
func (c *Client) Verify(ctx context.Context, token string) (Claims, error) {
	var cl idClaims
	if err := c.keys.Verify(ctx, token, &cl); err != nil {
		if errors.Is(err, jwks.ErrRejected) {
			return Claims{}, fmt.Errorf("%w: id token: %w", ErrRejected, err)
		}
		return Claims{}, fmt.Errorf("googleauth: %w", err)
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

func (c *Client) fetchJSON(req *http.Request, out any) (int, error) {
	ctx, cancel := context.WithTimeout(req.Context(), providerTimeout)
	defer cancel()
	resp, err := c.cfg.HTTP.Do(req.WithContext(ctx))
	if err != nil {
		return 0, fmt.Errorf("googleauth: %s: %w", req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, fmt.Errorf("googleauth: read %s: %w", req.URL.Host, err)
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil && resp.StatusCode == http.StatusOK {
			return 0, fmt.Errorf("googleauth: unreadable answer from %s: %w", req.URL.Host, err)
		}
	}
	return resp.StatusCode, nil
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
