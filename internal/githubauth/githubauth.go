// Package githubauth runs the server half of GitHub sign-in: it builds the
// authorization URL, redeems the code the browser brings back, and reads the
// account's numeric id and its primary verified email address.
package githubauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
)

// GitHub's published endpoints. Config carries them as fields so a test can
// point a client at a local server.
const (
	GitHubAuthURL   = "https://github.com/login/oauth/authorize"
	GitHubTokenURL  = "https://github.com/login/oauth/access_token"
	GitHubUserURL   = "https://api.github.com/user"
	GitHubEmailsURL = "https://api.github.com/user/emails"
)

const (
	// safety: a provider that stopped answering fails the sign-in instead of holding the request.
	providerTimeout = 10 * time.Second
	maxBody         = 1 << 20
)

// Errors a sign-in can fail with. ErrRejected covers every answer that means
// the code or token does not prove who the caller says they are.
var (
	ErrRejected   = errors.New("githubauth: sign-in rejected")
	ErrUnverified = errors.New("githubauth: the account has no primary email address GitHub has verified")
)

// Config names the OAuth app and where GitHub answers.
type Config struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserURL      string
	EmailsURL    string
	HTTP         *http.Client
}

// GitHub returns the configuration for GitHub's production endpoints.
func GitHub(clientID, clientSecret string) Config {
	return Config{
		ClientID: clientID, ClientSecret: clientSecret,
		AuthURL: GitHubAuthURL, TokenURL: GitHubTokenURL, UserURL: GitHubUserURL, EmailsURL: GitHubEmailsURL,
	}
}

// Profile is what GitHub asserts about the account that signed in. Subject is
// the numeric account id, because a login can be renamed and then taken by
// somebody else. Email is the primary address and is always one GitHub has
// verified.
type Profile struct {
	Subject string
	Login   string
	Name    string
	Email   string
}

// Client talks to one OAuth app registration.
type Client struct{ cfg Config }

// New returns a client for cfg.
func New(cfg Config) *Client {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: providerTimeout}
	}
	return &Client{cfg: cfg}
}

// AuthorizeURL is where the browser goes to sign in. It asks for the profile
// and the address list only.
func (c *Client) AuthorizeURL(state, verifier, redirectURI string) string {
	q := url.Values{
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {"read:user user:email"},
		"state":                 {state},
		"code_challenge":        {googleauth.CodeChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"allow_signup":          {"true"},
	}
	return c.cfg.AuthURL + "?" + q.Encode()
}

// Exchange redeems code and returns the profile of the account it belongs to.
func (c *Client) Exchange(ctx context.Context, code, verifier, redirectURI string) (Profile, error) {
	form := url.Values{
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Profile{}, fmt.Errorf("githubauth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// hack: GitHub answers form-encoded unless asked for JSON.
	req.Header.Set("Accept", "application/json")
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	status, err := c.fetchJSON(req, &tok)
	if err != nil {
		return Profile{}, err
	}
	if tok.Error != "" || status == http.StatusBadRequest || status == http.StatusUnauthorized {
		return Profile{}, fmt.Errorf("%w: github refused the code: %s", ErrRejected, tok.Error)
	}
	if status != http.StatusOK {
		return Profile{}, fmt.Errorf("githubauth: token endpoint answered %d", status)
	}
	if tok.AccessToken == "" {
		return Profile{}, fmt.Errorf("%w: github returned no access token", ErrRejected)
	}
	return c.profile(ctx, tok.AccessToken)
}

func (c *Client) profile(ctx context.Context, accessToken string) (Profile, error) {
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := c.get(ctx, c.cfg.UserURL, accessToken, &user); err != nil {
		return Profile{}, err
	}
	if user.ID <= 0 {
		return Profile{}, fmt.Errorf("%w: github returned no account id", ErrRejected)
	}
	var addresses []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := c.get(ctx, c.cfg.EmailsURL, accessToken, &addresses); err != nil {
		return Profile{}, err
	}
	p := Profile{Subject: strconv.FormatInt(user.ID, 10), Login: user.Login, Name: user.Name}
	// safety: only the primary address GitHub has verified counts; the profile's public email and any
	// secondary address are ignored, because linking on an unproven address is an account takeover.
	for _, a := range addresses {
		if a.Primary && a.Verified {
			p.Email = strings.ToLower(strings.TrimSpace(a.Email))
		}
	}
	if p.Email == "" {
		return Profile{}, ErrUnverified
	}
	return p, nil
}

func (c *Client) get(ctx context.Context, endpoint, accessToken string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("githubauth: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	status, err := c.fetchJSON(req, out)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("%w: github refused the token", ErrRejected)
	}
	if status != http.StatusOK {
		return fmt.Errorf("githubauth: %s answered %d", req.URL.Path, status)
	}
	return nil
}

func (c *Client) fetchJSON(req *http.Request, out any) (int, error) {
	ctx, cancel := context.WithTimeout(req.Context(), providerTimeout)
	defer cancel()
	resp, err := c.cfg.HTTP.Do(req.WithContext(ctx))
	if err != nil {
		return 0, fmt.Errorf("githubauth: %s: %w", req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, fmt.Errorf("githubauth: read %s: %w", req.URL.Host, err)
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil && resp.StatusCode == http.StatusOK {
			return 0, fmt.Errorf("githubauth: unreadable answer from %s: %w", req.URL.Host, err)
		}
	}
	return resp.StatusCode, nil
}
