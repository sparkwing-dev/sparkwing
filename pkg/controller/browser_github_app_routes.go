package controller

import (
	"context"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// GitHubConnectWriter rechecks session authority and linked identity when connecting an installation.
type GitHubConnectWriter interface {
	Team() store.Team
	LinkedGitHubSubject(context.Context, capability.Authorized) (string, error)
	ConsumeGitHubConnect(context.Context, capability.Authorized, string, time.Time) error
	BindAuthorizedGitHubInstallation(context.Context, capability.Authorized, store.GitHubAppInstallation) (store.GitHubAppInstallation, error)
}

var _ GitHubConnectWriter = (*store.Tenant)(nil)

// WithGitHubConnectWriter supplies tenant installation connection transactions.
func (s *Server) WithGitHubConnectWriter(resolve func(context.Context, store.Team) (GitHubConnectWriter, error)) *Server {
	s.resolveGitHubConnectWriter = resolve
	return s
}

func (s *Server) githubConnectWriter(ctx context.Context, a capability.Authorized) (GitHubConnectWriter, error) {
	if a.Target.Team == "" {
		return nil, missingEndpointPolicy()
	}
	var writer GitHubConnectWriter
	var err error
	if s.resolveGitHubConnectWriter != nil {
		writer, err = s.resolveGitHubConnectWriter(ctx, store.Team(a.Target.Team))
	} else if s.store != nil {
		var tenant *store.Tenant
		tenant, err = s.store.ForTeam(ctx, store.Team(a.Target.Team))
		writer = tenant
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if writer == nil || writer.Team() != store.Team(a.Target.Team) {
		return nil, missingEndpointPolicy()
	}
	return writer, nil
}

type githubBrowserProofKey struct{}

type githubBrowserProof struct {
	authorized capability.Authorized
	csrfToken  string
}

func (s *Server) githubBrowserEndpoints() []Endpoint {
	return []Endpoint{
		s.githubBrowserEndpoint("POST", "/github/app/connect", s.verifyGitHubAppBrowser, s.handleGitHubAppConnect(false)),
		s.githubBrowserEndpoint("POST", "/github/app/connect/existing", s.verifyGitHubAppBrowser, s.handleGitHubAppConnect(true)),
		s.githubBrowserEndpoint("GET", "/github/app/setup", s.verifyGitHubAppReturn, s.handleGitHubAppSetup),
		s.githubBrowserEndpoint("GET", githubAppCallbackPath, s.verifyGitHubAppReturn, s.handleGitHubAppCallback),
		s.githubBrowserEndpoint("GET", githubAppCompletePath, s.verifyGitHubAppBrowser, s.handleGitHubAppComplete),
		s.githubBrowserEndpoint("GET", githubAppAvailablePath, s.verifyGitHubAppBrowser, s.handleGitHubAppAvailable),
		s.githubBrowserEndpoint("POST", "/github/app/select", s.verifyGitHubAppBrowser, s.handleGitHubAppSelect),
	}
}

func (s *Server) githubBrowserEndpoint(method, pattern string, verify func(*http.Request) error, handle http.HandlerFunc) Endpoint {
	return Endpoint{
		Method: method, Pattern: pattern, Authentication: capability.AuthBrowserFlow, Verify: verify,
		Handle: func(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
			handle(w, r)
			return nil
		},
	}
}

func (s *Server) verifyGitHubAppBrowser(r *http.Request) error {
	if len(r.Header.Values("Authorization")) != 0 {
		return sessionUnavailable()
	}
	session, err := s.readSessionFlow(r)
	if err != nil {
		return err
	}
	if s.authorization.Authenticator == nil || s.authorization.Bindings == nil {
		return missingEndpointPolicy()
	}
	route, _ := capability.LookupRoute("PUT", "/api/v1/team/github-app/extra-repos")
	authenticated, err := s.authorization.Authenticator.Authenticate(r.Context(), capability.Presentation{
		Class: capability.CredentialSession, Raw: session.ID,
		Evidence: capability.RequestEvidence{Target: capability.Target{Team: string(session.Team)}},
	}, route)
	if err != nil {
		return err
	}
	p := authenticated.Principal
	if p.Class != capability.CredentialSession || p.Capability.Subject.Kind != capability.SubjectAccount ||
		p.Capability.Subject.ID != session.AccountID || p.Capability.Team != string(session.Team) {
		return sessionUnavailable()
	}
	a, err := capability.Authorize(r.Context(), s.authorization.Bindings, route, authenticated, capability.Target{Team: string(session.Team)})
	if err != nil {
		return err
	}
	if unsafeMethod(r.Method) {
		if err := s.readGitHubAppForm(r, session.CSRFToken); err != nil {
			return err
		}
	}
	if s.githubApp == nil {
		return &apierr.Error{Code: apierr.NotFound, Message: errNoGitHubApp.Error()}
	}
	if _, err := s.githubConnectWriter(r.Context(), a); err != nil {
		return err
	}
	notePolicyPrincipal(r.Context(), p)
	*r = *r.WithContext(context.WithValue(r.Context(), githubBrowserProofKey{}, githubBrowserProof{authorized: a, csrfToken: session.CSRFToken}))
	return nil
}

func (s *Server) readGitHubAppForm(r *http.Request, csrf string) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		return invalidEngineBody("invalid GitHub connection form")
	}
	if len(r.CookiesNamed(cookieName(csrfCookieName, s.cookiesSecure()))) != 1 || len(r.PostForm["csrf_token"]) != 1 ||
		!validFormCSRF(r, s.cookiesSecure()) || !constantTimeEqual(r.PostForm.Get("csrf_token"), csrf) {
		return apierr.Authorization(apierr.ReasonReauthRequired, "invalid CSRF token")
	}
	for key, values := range r.PostForm {
		if len(values) != 1 || (key != "csrf_token" && (key != "installation_id" || r.URL.Path != "/github/app/select")) {
			return invalidEngineBody("invalid GitHub connection form")
		}
	}
	return nil
}

func (s *Server) verifyGitHubAppReturn(r *http.Request) error {
	if len(r.Header.Values("Authorization")) != 0 {
		return sessionUnavailable()
	}
	query := r.URL.Query()
	for _, values := range query {
		if len(values) != 1 {
			return invalidEngineBody("invalid GitHub connection return")
		}
	}
	if r.URL.Path == "/github/app/setup" && (query.Get("setup_action") == "request" || (query.Get("setup_action") == "update" && query.Get("state") == "")) {
		return nil
	}
	flow, ok := s.readGitHubAppFlow(r)
	if !ok || s.githubApp == nil || !constantTimeEqual(query.Get("state"), flow.State) {
		return apierr.Authorization(apierr.ReasonReauthRequired, errConnectState.Error())
	}
	var state githubConnectState
	if !openFlowState(s.githubApp.stateKey, flow.State, &state) || state.Nonce == "" || time.Now().Unix() >= state.Expires ||
		!constantTimeEqual(state.Verifier, verifierDigest(flow.Verifier)) {
		return apierr.Authorization(apierr.ReasonReauthRequired, errConnectState.Error())
	}
	return nil
}
