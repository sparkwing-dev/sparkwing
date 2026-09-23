package controller

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// SourceTokenResponse is a short-lived GitHub credential that reads one
// repository's contents, for the runner holding a claim on a run of that
// repository. The runner hands it to git in the fetch's environment only.
type SourceTokenResponse struct {
	Token      string `json:"token"`
	ExpiresAt  int64  `json:"expires_at"`
	Repository string `json:"repository"`
	// ExtraRepositories are the repositories a team owner listed for
	// Repository, which the token also reads.
	ExtraRepositories []string `json:"extra_repositories,omitempty"`
}

func runGitHubRepo(trigger *store.Trigger) (store.GitHubRepo, bool) {
	identity, err := sourceurl.TriggerRepository(trigger.RepoURL, trigger.TriggerEnv["GITHUB_REPOSITORY"],
		trigger.GithubOwner, trigger.GithubRepo)
	if err != nil || identity == "" {
		return store.GitHubRepo{}, false
	}
	slug, ok := strings.CutPrefix(identity, "github.com/")
	if !ok {
		return store.GitHubRepo{}, false
	}
	return store.ParseGitHubRepo(slug)
}

// safety: the token reads only the run's own repository, only a runner holding
// live work on the run gets one, and only when an installation the run's team
// holds covers that repository, so naming a repository in a run proves nothing
// by itself.
func (s *Server) handleRunSourceToken(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	runID := r.PathValue("id")
	src, ok := s.claimedRunSource(w, r, runID)
	if !ok {
		return
	}
	repo, ok := runGitHubRepo(src.trigger)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("run "+runID+" names no GitHub repository"))
		return
	}
	out, failure := s.runAppToken(r, src, repo, nil)
	if failure != nil {
		failure.write(w)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// claimedRunSource is the run a caller holding a live claim on runID asks a
// source credential for, or false once the refusal is written.
type claimedRunSource struct {
	claimed store.ClaimedRun
	tenant  *store.Tenant
	trigger *store.Trigger
}

// safety: the claim is looked up for this credential's token prefix on every
// call, and the token's own team must be the run's, so a runner never reads a
// credential for work it no longer holds or for another team's run.
func (s *Server) claimedRunSource(w http.ResponseWriter, r *http.Request, runID string) (claimedRunSource, bool) {
	p, _ := PrincipalFromContext(r.Context())
	claimed, err := s.store.ClaimedRunFor(r.Context(), runID, claimIdentity(r), time.Now())
	if errors.Is(err, store.ErrNotFound) || (err == nil && p != nil && p.Team != "" && store.NormalizeTeam(p.Team) != claimed.Team) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "claim_required", Principal: p.label(),
			Message: "run " + runID + " is not claimed by this principal",
		})
		return claimedRunSource{}, false
	}
	if err != nil {
		s.writeInternalError(w, r, "source credential claim", err)
		return claimedRunSource{}, false
	}
	tenant, err := s.tenantForTeam(r.Context(), claimed.Team)
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		writeError(w, http.StatusNotFound, runNotFound(runID))
		return claimedRunSource{}, false
	}
	if err != nil {
		s.writeInternalError(w, r, "source credential team", err)
		return claimedRunSource{}, false
	}
	trigger, err := s.store.GetTrigger(r.Context(), runID)
	if err != nil || store.NormalizeTeam(trigger.Team) != claimed.Team {
		writeError(w, http.StatusNotFound, runNotFound(runID))
		return claimedRunSource{}, false
	}
	return claimedRunSource{claimed: claimed, tenant: tenant, trigger: trigger}, true
}

// sourceFailure is a refusal a source credential route answers with.
type sourceFailure struct {
	status     int
	err        error
	retryAfter time.Duration
	// notCovered marks the refusal that lets the git-credential route go on
	// to the team's stored credential.
	notCovered bool
}

func (f *sourceFailure) write(w http.ResponseWriter) {
	if f.retryAfter > 0 {
		setRetryAfter(w, f.retryAfter)
	}
	writeError(w, f.status, f.err)
}

// runAppToken mints the run's App token, read-only and restricted to repo
// and the extra repositories a team owner listed for it, all of which one
// installation the run's team holds must cover now, not only when the list
// was set.
func (s *Server) runAppToken(r *http.Request, src claimedRunSource, repo store.GitHubRepo, extra []store.GitHubRepo) (SourceTokenResponse, *sourceFailure) {
	runID := src.trigger.ID
	notCovered := func(slug string) *sourceFailure {
		return &sourceFailure{
			status: http.StatusNotFound, notCovered: true,
			err: errors.New("no GitHub App installation this team holds covers " + slug),
		}
	}
	inst, found, err := s.teamInstallationFor(r.Context(), src.tenant, repo)
	if err != nil {
		s.logger.Warn("source token installation", "run_id", runID, "repository", repo.Slug(), "err", err.Error())
		return SourceTokenResponse{}, &sourceFailure{
			status: http.StatusBadGateway,
			err:    errors.New("GitHub could not be reached to find the repository's installation"),
		}
	}
	if !found {
		return SourceTokenResponse{}, notCovered(repo.Slug())
	}
	names := []string{repo.Name}
	slugs := []string{repo.Slug()}
	for _, x := range extra {
		// safety: the token is minted from the installation that covers the
		// run's repository, so every extra repository must be covered by that
		// same installation of the team's, not merely by some installation.
		xinst, covered, err := s.teamInstallationFor(r.Context(), src.tenant, x)
		if err != nil {
			s.logger.Warn("source token installation", "run_id", runID, "repository", x.Slug(), "err", err.Error())
			return SourceTokenResponse{}, &sourceFailure{
				status: http.StatusBadGateway,
				err:    errors.New("GitHub could not be reached to find the installation of " + x.Slug()),
			}
		}
		if !covered || xinst.InstallationID != inst.InstallationID {
			return SourceTokenResponse{}, &sourceFailure{
				status: http.StatusForbidden,
				err: errors.New("the team's extra repositories for " + repo.Slug() + " name " + x.Slug() +
					", which the installation covering it no longer covers; a team owner updates the list (Team > GitHub)"),
			}
		}
		names = append(names, x.Name)
		slugs = append(slugs, x.Slug())
	}
	id := claimIdentity(r)
	key := runID + "\x00" + id.Principal + "\x00" + id.TokenPrefix + "\x00" + strings.ToLower(strings.Join(slugs, ","))
	cached, refused := s.githubApp.reuseSourceToken(key, time.Now())
	if refused {
		return SourceTokenResponse{}, &sourceFailure{
			status: http.StatusTooManyRequests, retryAfter: time.Minute,
			err: errors.New("this claim asked for source tokens too often"),
		}
	}
	if cached != nil {
		return *cached, nil
	}
	tok, err := s.githubApp.client.InstallationToken(r.Context(), inst.InstallationID, names,
		map[string]string{"contents": "read"})
	if errors.Is(err, githubapp.ErrNotInstalled) {
		return SourceTokenResponse{}, notCovered(strings.Join(slugs, ", "))
	}
	if err != nil {
		s.logger.Warn("source token mint", "run_id", runID, "repository", repo.Slug(), "err", err.Error())
		return SourceTokenResponse{}, &sourceFailure{
			status: http.StatusBadGateway,
			err:    errors.New("GitHub did not issue a token for " + repo.Slug()),
		}
	}
	p, _ := PrincipalFromContext(r.Context())
	s.logger.Info("source token minted", "team", string(src.claimed.Team), "run_id", runID,
		"repositories", strings.Join(slugs, ","), "installation_id", inst.InstallationID, "principal", p.label())
	out := SourceTokenResponse{Token: tok.Token, ExpiresAt: tok.ExpiresAt.Unix(), Repository: repo.Slug(), ExtraRepositories: slugs[1:]}
	s.githubApp.keepSourceToken(key, out)
	return out, nil
}

// safety: a claim holder gets one live token, reused until shortly before it
// expires, and a claim asking in a loop is refused, so a runner cannot turn
// its claim into a stream of GitHub tokens or spend the App's rate limit.
const (
	sourceTokenReuseMargin = 5 * time.Minute
	sourceTokenCallsPerMin = 10
)

type sourceTokenEntry struct {
	token  *SourceTokenResponse
	window time.Time
	calls  int
}

func (a *githubAppState) reuseSourceToken(key string, now time.Time) (cached *SourceTokenResponse, refused bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, e := range a.sources {
		expired := e.token == nil || now.After(time.Unix(e.token.ExpiresAt, 0))
		if expired && now.Sub(e.window) > time.Minute {
			delete(a.sources, k)
		}
	}
	e := a.sources[key]
	if e == nil {
		e = &sourceTokenEntry{window: now}
		a.sources[key] = e
	}
	if now.Sub(e.window) > time.Minute {
		e.window, e.calls = now, 0
	}
	e.calls++
	if e.calls > sourceTokenCallsPerMin {
		return nil, true
	}
	if e.token != nil && time.Unix(e.token.ExpiresAt, 0).Sub(now) > sourceTokenReuseMargin {
		return e.token, false
	}
	return nil, false
}

func (a *githubAppState) keepSourceToken(key string, tok SourceTokenResponse) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.sources[key]; e != nil {
		e.token = &tok
	}
}
