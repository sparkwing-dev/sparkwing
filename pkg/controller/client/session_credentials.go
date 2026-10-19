package client

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// NewSessionRunnerTokenManager uses an existing account session without converting bearer authority.
func NewSessionRunnerTokenManager(ctx context.Context, options EngineOptions, session string) (RunnerTokenManager, error) {
	c, err := newSessionEngineClient(ctx, options, session)
	if err != nil {
		return nil, err
	}
	return &tokenManager{client: c, base: "/api/v1/tokens"}, nil
}

// NewSessionTeamSettingsClient presents an account session, for changes only the owner's own sign-in may make.
func NewSessionTeamSettingsClient(ctx context.Context, options EngineOptions, session string) (*TeamSettingsClient, error) {
	c, err := newSessionEngineClient(ctx, options, session)
	if err != nil {
		return nil, err
	}
	return &TeamSettingsClient{client: c}, nil
}

func newSessionEngineClient(ctx context.Context, options EngineOptions, session string) (*engineClient, error) {
	if !validSessionValue(session) || options.Bearer != nil {
		return nil, &apierr.Error{Code: apierr.BadRequest, Message: "session requests require only an account-session presentation"}
	}
	c, err := newEngineClient(options)
	if err != nil {
		return nil, err
	}
	transport := c.http.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	base, _ := url.Parse(c.base)
	auth := &runnerSessionTransport{base: base, session: session, transport: transport}
	c.http.Transport = auth
	c.http.Jar = nil
	var proof struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := c.request(ctx, "GET", "/api/v1/auth/session", "/api/v1/auth/session", "", nil, &proof); err != nil {
		return nil, err
	}
	if !validSessionValue(proof.CSRFToken) || strings.ContainsAny(proof.CSRFToken, "\";,\\") {
		return nil, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned no valid session CSRF proof"}
	}
	auth.csrf = proof.CSRFToken
	return c, nil
}

func validSessionValue(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		if value[i] <= ' ' || value[i] >= 127 {
			return false
		}
	}
	return true
}

type runnerSessionTransport struct {
	base          *url.URL
	session, csrf string
	transport     http.RoundTripper
}

func (t *runnerSessionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != t.base.Scheme || request.URL.Host != t.base.Host || !strings.HasPrefix(request.URL.Path, strings.TrimRight(t.base.Path, "/")+"/") {
		return nil, &apierr.Error{Code: apierr.Forbidden, Message: "session credentials cannot leave the selected controller"}
	}
	r := request.Clone(request.Context())
	r.Header.Set("Authorization", "Session "+t.session)
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		r.Header.Set("Origin", t.base.Scheme+"://"+t.base.Host)
		r.Header.Set("X-CSRF-Token", t.csrf)
		cookie := "__Host-sw_csrf"
		if t.base.Scheme == "http" {
			cookie = "sw_csrf"
		}
		r.Header.Set("Cookie", (&http.Cookie{Name: cookie, Value: t.csrf}).String())
	}
	return t.transport.RoundTrip(r)
}
