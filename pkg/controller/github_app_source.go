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
	p, _ := PrincipalFromContext(r.Context())
	claimed, err := s.store.ClaimedRunFor(r.Context(), runID, claimIdentity(r), time.Now())
	if errors.Is(err, store.ErrNotFound) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "claim_required", Principal: p.label(),
			Message: "run " + runID + " is not claimed by this principal",
		})
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "source token claim", err)
		return
	}
	tenant, err := s.tenantForTeam(r.Context(), claimed.Team)
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		writeError(w, http.StatusNotFound, runNotFound(runID))
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "source token team", err)
		return
	}
	trigger, err := s.store.GetTrigger(r.Context(), runID)
	if err != nil || store.NormalizeTeam(trigger.Team) != claimed.Team {
		writeError(w, http.StatusNotFound, runNotFound(runID))
		return
	}
	repo, ok := runGitHubRepo(trigger)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("run "+runID+" names no GitHub repository"))
		return
	}
	inst, found, err := s.teamInstallationFor(r.Context(), tenant, repo)
	if err != nil {
		s.logger.Warn("source token installation", "run_id", runID, "repository", repo.Slug(), "err", err.Error())
		writeError(w, http.StatusBadGateway, errors.New("GitHub could not be reached to find the repository's installation"))
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("no GitHub App installation this team holds covers "+repo.Slug()))
		return
	}
	key := runID + "\x00" + claimIdentity(r).Principal + "\x00" + claimIdentity(r).TokenPrefix
	cached, refused := s.githubApp.reuseSourceToken(key, time.Now())
	if refused {
		setRetryAfter(w, time.Minute)
		writeError(w, http.StatusTooManyRequests, errors.New("this claim asked for source tokens too often"))
		return
	}
	if cached != nil {
		writeJSON(w, http.StatusOK, *cached)
		return
	}
	tok, err := s.githubApp.client.InstallationToken(r.Context(), inst.InstallationID, repo.Name,
		map[string]string{"contents": "read"})
	if errors.Is(err, githubapp.ErrNotInstalled) {
		writeError(w, http.StatusNotFound, errors.New("no GitHub App installation this team holds covers "+repo.Slug()))
		return
	}
	if err != nil {
		s.logger.Warn("source token mint", "run_id", runID, "repository", repo.Slug(), "err", err.Error())
		writeError(w, http.StatusBadGateway, errors.New("GitHub did not issue a token for "+repo.Slug()))
		return
	}
	s.logger.Info("source token minted", "team", string(claimed.Team), "run_id", runID,
		"repository", repo.Slug(), "installation_id", inst.InstallationID, "principal", p.label())
	out := SourceTokenResponse{Token: tok.Token, ExpiresAt: tok.ExpiresAt.Unix(), Repository: repo.Slug()}
	s.githubApp.keepSourceToken(key, out)
	writeJSON(w, http.StatusOK, out)
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
