package controller

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	oidcTokensPerRun  = 30
	oidcTokenRefill   = 5 * time.Minute
	oidcWebhookSource = "github"
	oidcTriggerPush   = "push"
	oidcTriggerPR     = "pull_request"
	oidcTriggerCron   = "cron"
	oidcTriggerManual = "manual"
	oidcRunnerGitHub  = "github-actions"
	githubEventPush   = "push"
)

type oidcState struct {
	issuer *oidcissuer.Issuer
	limit  *ratelimit.Limiter
}

// OIDCTokenRequest is the body of POST /api/v1/runs/{id}/oidc-token.
type OIDCTokenRequest struct {
	// Audience becomes the token's aud claim: the value the relying party
	// checks, such as sts.amazonaws.com.
	Audience string `json:"audience"`
}

// OIDCTokenResponse carries a signed ID token for one run.
type OIDCTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// WithOIDCIssuer makes this controller an OpenID Connect issuer: it serves
// the discovery document and key set, and signs ID tokens for the runs a
// caller holds a claim on. A nil issuer leaves all three routes answering
// 404.
func (s *Server) WithOIDCIssuer(iss *oidcissuer.Issuer) *Server {
	s.oidc = oidcState{issuer: iss, limit: ratelimit.New(oidcTokensPerRun, oidcTokenRefill)}
	return s
}

func (s *Server) handleOIDCDiscovery(w http.ResponseWriter, r *http.Request) {
	if s.oidc.issuer == nil {
		writeError(w, http.StatusNotFound, errOIDCOff)
		return
	}
	s.oidc.issuer.ServeDiscovery(w, r)
}

func (s *Server) handleOIDCKeys(w http.ResponseWriter, r *http.Request) {
	if s.oidc.issuer == nil {
		writeError(w, http.StatusNotFound, errOIDCOff)
		return
	}
	s.oidc.issuer.ServeJWKS(w, r)
}

var errOIDCOff = errors.New("this controller holds no OIDC signing key, so it issues no ID tokens")

// safety: every claim but the audience is read from the rows of the run the
// caller holds a live claim on, never from the request, so a runner can
// speak only for the work it is executing.
func (s *Server) handleOIDCToken(w http.ResponseWriter, r *http.Request) {
	if s.oidc.issuer == nil {
		writeError(w, http.StatusNotFound, errOIDCOff)
		return
	}
	var body OIDCTokenRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := oidcissuer.ValidateAudience(body.Audience); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runID := r.PathValue("id")
	p, _ := PrincipalFromContext(r.Context())
	now := time.Now().Round(0)
	claimed, err := s.store.ClaimedRunFor(r.Context(), runID, claimIdentity(r), now)
	if errors.Is(err, store.ErrNotFound) {
		// safety: one answer for a run in another team, a run nobody claimed,
		// and no run at all, so the route reveals no other run.
		writeError(w, http.StatusNotFound, errors.New("run "+runID+" holds no live claim by this credential"))
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "oidc token: resolve claimed run", err)
		return
	}
	if claimed.Team == "" {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "no_team", Principal: p.label(), Message: "the claimed run belongs to no team",
		})
		return
	}
	limitKey := claimIdentity(r).TokenPrefix + "\x00" + runID
	if ok, wait := s.oidc.limit.AllowWithRetry(limitKey, now); !ok {
		setRetryAfter(w, wait)
		writeError(w, http.StatusTooManyRequests, errors.New("this claim has minted its ID token budget for the run; retry later"))
		return
	}
	tn, err := s.store.ForTeam(r.Context(), claimed.Team)
	if err != nil {
		s.writeInternalError(w, r, "oidc token: resolve team", err)
		return
	}
	run, err := tn.GetRun(r.Context(), runID)
	if err != nil {
		s.writeInternalError(w, r, "oidc token: read run", err)
		return
	}
	trig, err := s.store.GetTrigger(r.Context(), runID)
	if errors.Is(err, store.ErrNotFound) {
		trig = nil
	} else if err != nil {
		s.writeInternalError(w, r, "oidc token: read trigger", err)
		return
	}
	claims := oidcClaimsFor(claimed, run, trig, p)
	claims.Audience = body.Audience
	var notAfter time.Time
	if p != nil {
		notAfter = p.Expires
	}
	token, exp, err := s.oidc.issuer.Mint(claims, now, notAfter)
	if errors.Is(err, oidcissuer.ErrInvalidClaim) {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	writeJSON(w, http.StatusOK, OIDCTokenResponse{Token: token, ExpiresAt: exp.UTC()})
}

// safety: the trigger row is preferred over the run row because it is
// written once at intake, where the source and repository fields are
// validated, and a later run upgrade does not rewrite it.
func oidcClaimsFor(claimed store.ClaimedRun, run *store.Run, trig *store.Trigger, p *Principal) oidcissuer.Claims {
	c := oidcissuer.Claims{
		Team:       string(claimed.Team),
		Pipeline:   claimed.Pipeline,
		Trigger:    oidcTriggerManual,
		RunnerKind: oidcRunnerKind(p),
		RunID:      run.ID,
	}
	branch, sha := run.GitBranch, run.GitSHA
	owner, repo, repoURL := run.GithubOwner, run.GithubRepo, run.RepoURL
	if trig != nil {
		c.Pipeline = trig.Pipeline
		branch, sha = trig.GitBranch, trig.GitSHA
		owner, repo, repoURL = trig.GithubOwner, trig.GithubRepo, trig.RepoURL
		// safety: the event name is reserved to the signed webhook, which records
		// it at intake; a github source without one proves neither event.
		event := trig.TriggerEnv[sparkwing.EnvGitHubEventName]
		switch {
		case trig.TriggerSource == oidcWebhookSource && event == githubEventPush:
			c.Trigger = oidcTriggerPush
		case trig.TriggerSource == oidcWebhookSource && event == sparkwing.EventPullRequest && pullNumber(trig.TriggerEnv) != "":
			c.Trigger = oidcTriggerPR
		// safety: submitters cannot set the schedule key, which the intake
		// strips, so a "schedule" source without it is a submitter's word.
		case trig.TriggerSource == cronTriggerSource && trig.TriggerEnv[crons.ScheduleEnvKey] != "":
			c.Trigger = oidcTriggerCron
		}
	}
	switch {
	case c.Trigger == oidcTriggerPR:
		// safety: a pull request's head branch may share a protected branch's
		// name, so its ref names the pull request and never refs/heads/.
		c.Ref = "refs/pull/" + pullNumber(trig.TriggerEnv) + "/head"
	case branch != "":
		c.Ref = "refs/heads/" + branch
	}
	c.SHA = sha
	c.Repository = canonicalRepository(owner, repo, repoURL)
	return c
}

func pullNumber(env map[string]string) string {
	n := env[sparkwing.EnvPRNumber]
	if n == "" || strings.Trim(n, "0123456789") != "" {
		return ""
	}
	return n
}

func oidcRunnerKind(p *Principal) string {
	if _, isGitHub, _ := githubRunnerScope(p); isGitHub {
		return oidcRunnerGitHub
	}
	if p == nil || p.Kind == "" {
		return store.TokenKindUser
	}
	return p.Kind
}

// safety: the owner and name the intake parsed win over the clone URL, which is free-form.
func canonicalRepository(owner, repo, repoURL string) string {
	if owner != "" && repo != "" {
		return "github.com/" + owner + "/" + repo
	}
	if repoURL == "" {
		return ""
	}
	raw := repoURL
	if host, path, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":"); ok && strings.HasPrefix(raw, "git@") {
		raw = "ssh://" + host + "/" + path
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	path := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if path == "" {
		return ""
	}
	return strings.ToLower(u.Hostname()) + "/" + path
}
