package controller

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a plan claim builds the pipeline and a work claim runs it, so both fetch source.
var claimSourceKinds = []store.ClaimTokenKind{store.ClaimTokenPlan, store.ClaimTokenWork}

// safety: a runner's node and trigger claims fetch the run's source too, and
// this route is the only one that hands out a source credential.
var claimSourceCredentialKinds = []store.ClaimTokenKind{
	store.ClaimTokenPlan, store.ClaimTokenWork, store.ClaimTokenNode, store.ClaimTokenTrigger,
}

const (
	gitCredentialGitHubApp  = "github_app"
	sourceCredentialsPerMin = 10
)

type claimedRunSource struct {
	tenant  *store.Tenant
	trigger *store.Trigger
	// safety: the zero identity marks a launcher's claim, which never
	// receives a team's stored credential.
	claimant store.ClaimIdentity
}

func (c claimedRunSource) machine() bool { return c.claimant.TokenPrefix != "" }

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

// safety: the claim token names its team and run, so the repository and
// commit come from the run's own trigger and never from the request. A
// runner's claim stops with the machine credential that made it.
func (s *Server) claimRunSource(w http.ResponseWriter, r *http.Request) (claimedRunSource, store.ClaimToken, bool) {
	tok, _ := claimTokenFromContext(r.Context())
	tenant, err := s.tenantForTeam(r.Context(), tok.Team)
	if err != nil {
		writeError(w, http.StatusNotFound, runNotFound(tok.RunID))
		return claimedRunSource{}, tok, false
	}
	trigger, err := tenant.GetTrigger(r.Context(), tok.RunID)
	if err != nil || store.NormalizeTeam(trigger.Team) != tok.Team {
		writeError(w, http.StatusNotFound, runNotFound(tok.RunID))
		return claimedRunSource{}, tok, false
	}
	claimant, err := tenant.SourceClaimant(r.Context(), tok)
	if err != nil {
		writeClaimRefusal(w, r, s, err)
		return claimedRunSource{}, tok, false
	}
	if claimant.TokenPrefix != "" {
		cred, err := s.store.LookupTokenByPrefix(claimant.TokenPrefix)
		if err != nil || cred == nil || cred.Team != tok.Team || cred.Principal != claimant.Principal || !cred.IsValid(time.Now()) {
			writeAuthError(w, http.StatusUnauthorized, authErrorBody{
				Code:    "unauthenticated",
				Message: "the credential that made this claim no longer authenticates",
			})
			return claimedRunSource{}, tok, false
		}
	}
	return claimedRunSource{tenant: tenant, trigger: trigger, claimant: claimant}, tok, true
}

// safety: the one route that hands out a source credential, only to a live claim on the run. The App token
// reads the run's repository by GitHub ID, and one the run names by hand qualifies only once the team
// subscribed it, so naming a repository widens nothing. A node's claim gets a few, none once it starts.
func (s *Server) handleRunSourceCredential(w http.ResponseWriter, r *http.Request) {
	src, tok, ok := s.claimRunSource(w, r)
	if !ok {
		return
	}
	t := src.trigger
	if !s.sourceCredentialLimit.allow(tok.Prefix, sourceCredentialsPerMin, time.Now()) {
		setRetryAfter(w, time.Minute)
		writeError(w, http.StatusTooManyRequests, errors.New("this claim asked for its source credential too often"))
		return
	}
	identity, err := sourceurl.TriggerRepository(t.RepoURL, t.TriggerEnv["GITHUB_REPOSITORY"], t.GithubOwner, t.GithubRepo)
	if err != nil || identity == "" {
		writeNoSourceCredential(w, "run "+t.ID+" names no repository to fetch")
		return
	}
	if !src.machine() && t.GitSHA == "" {
		writeError(w, http.StatusConflict, errors.New("run "+t.ID+" records no commit to check out"))
		return
	}
	host, _, _ := strings.Cut(identity, "/")
	repo, onGitHub := runGitHubRepo(t)
	var extra []store.GitHubRepo
	if onGitHub {
		if extra, err = src.tenant.GitHubAppExtraRepoRefs(r.Context(), repo.Slug()); err != nil {
			s.writeInternalError(w, r, "read extra repositories", err)
			return
		}
	}
	remedy := noSourceCredentialRemedy(identity, host)
	if onGitHub && s.githubApp != nil {
		cred, failure := s.mintSourceCredential(r, src, tok, repo, extra)
		if failure == nil {
			writeJSON(w, http.StatusOK, cred)
			return
		}
		if !failure.notCovered {
			failure.write(w)
			return
		}
		if !src.machine() {
			writeNoSourceCredential(w, "Sparkwing Cloud fetches source only through the team's GitHub App: "+
				failure.err.Error()+"; stored git credentials serve self-hosted runners only")
			return
		}
		remedy = failure.err.Error() + ", or store a git credential for github.com (Team > Git credentials)"
	}
	if !src.machine() {
		writeNoSourceCredential(w, cloudSourceNeedsApp(t.ID))
		return
	}
	var repos []string
	if onGitHub {
		repos = append([]string{repo.Slug()}, repoSlugs(extra)...)
	}
	s.releaseTeamGitCredential(w, r, src, tok, host, repos, remedy)
}

type sourceFailure struct {
	status int
	code   string
	err    error
	// safety: only this failure lets a runner's claim fall back to the team's
	// stored credential; a launcher's claim fetches only through the App.
	notCovered bool
}

func (f *sourceFailure) write(w http.ResponseWriter) {
	if f.code != "" {
		writeAuthError(w, f.status, authErrorBody{Code: f.code, Message: f.err.Error()})
		return
	}
	writeError(w, f.status, f.err)
}

func notCoveredFailure(msg string) *sourceFailure {
	return &sourceFailure{status: http.StatusNotFound, err: errors.New(msg), notCovered: true}
}

// safety: the token is minted for repository IDs only: the trigger's, which
// the App's webhook recorded, or else the one the team's App trigger
// subscription recorded for the name the run gives.
func (s *Server) mintSourceCredential(r *http.Request, src claimedRunSource, tok store.ClaimToken,
	repo store.GitHubRepo, extra []store.GitHubRepo,
) (store.SourceCredential, *sourceFailure) {
	ctx := r.Context()
	t := src.trigger
	repoID := t.GithubRepoID
	if repoID == 0 {
		id, err := src.tenant.GitHubAppSubscribedRepository(ctx, repo.Slug())
		if errors.Is(err, store.ErrNotFound) {
			return store.SourceCredential{}, notCoveredFailure("run " + t.ID + " names " + repo.Slug() +
				" by hand, and the team's GitHub App reads such a repository only once the team subscribes it (Team > GitHub)")
		}
		if err != nil {
			return store.SourceCredential{}, s.sourceInternalFailure(r, "read the team's subscriptions", err)
		}
		repoID = id
	}
	inst, covered, err := s.teamInstallationFor(ctx, src.tenant, repo)
	if err != nil {
		s.logger.Warn("source credential installation", "run_id", t.ID, "repository", repo.Slug(), "err", err.Error())
		return store.SourceCredential{}, &sourceFailure{
			status: http.StatusBadGateway,
			err:    errors.New("GitHub could not be reached to find the repository's installation"),
		}
	}
	if !covered {
		return store.SourceCredential{}, notCoveredFailure("no source credential covers github.com/" + repo.Slug() +
			": install the team's GitHub App on " + repo.Slug() + " (Team > GitHub)")
	}
	ids, slugs := []int64{repoID}, []string{repo.Slug()}
	for _, x := range extra {
		xinst, xcovered, err := s.teamInstallationFor(ctx, src.tenant, x)
		if err != nil || !xcovered || xinst.InstallationID != inst.InstallationID || x.ID == 0 {
			return store.SourceCredential{}, &sourceFailure{status: http.StatusConflict, err: errors.New("extra repository " +
				x.Slug() + " is not covered by the installation covering " + repo.Slug() + " or has no recorded id; " +
				"a team owner saves the list again (Team > GitHub)")}
		}
		ids, slugs = append(ids, x.ID), append(slugs, x.Slug())
	}
	spend, err := src.tenant.SpendSourceCredential(ctx, tok, s.sourceRelease(r, src, tok, gitCredentialGitHubApp, "github.com"), time.Now())
	if failure := s.sourceSpendFailure(r, err); failure != nil {
		return store.SourceCredential{}, failure
	}
	minted, err := s.githubApp.client.InstallationTokenByIDs(ctx, inst.InstallationID, ids, map[string]string{"contents": "read"})
	if err != nil {
		s.logger.Warn("source credential mint", "run_id", t.ID, "repository", repo.Slug(), "err", err.Error())
		return store.SourceCredential{}, &sourceFailure{
			status: http.StatusBadGateway,
			err:    errors.New("GitHub did not issue a token for " + repo.Slug() + "; ask again"),
		}
	}
	s.logger.Info("source credential minted", "team", string(tok.Team), "run_id", t.ID, "node_id", tok.NodeID,
		"kind", string(tok.Kind), "repositories", strings.Join(slugs, ","), "installation_id", inst.InstallationID)
	return store.SourceCredential{
		Kind: gitCredentialGitHubApp, Host: "github.com", Token: minted.Token, ExpiresAt: minted.ExpiresAt.Unix(),
		RepoURL: "https://github.com/" + repo.Slug() + ".git", SHA: t.GitSHA, Branch: t.GitBranch,
		Repositories: slugs, Source: spend.Spec,
	}, nil
}

// safety: a runner's release names its machine credential, so an owner's
// audit names the machine that received it; a launcher's names its claim.
func (s *Server) sourceRelease(r *http.Request, src claimedRunSource, tok store.ClaimToken, credentialID, host string) store.GitCredentialRelease {
	rel := store.GitCredentialRelease{CredentialID: credentialID, Host: host, Runner: src.claimant.Principal, TokenPrefix: src.claimant.TokenPrefix}
	if !src.machine() {
		p, _ := PrincipalFromContext(r.Context())
		rel.Runner, rel.TokenPrefix = p.label(), tok.Prefix
	}
	return rel
}

func (s *Server) sourceSpendFailure(r *http.Request, err error) *sourceFailure {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrSourceCredentialSpent):
		return &sourceFailure{status: http.StatusForbidden, code: "source_credential_spent", err: err}
	case errors.Is(err, store.ErrClaimNotLive):
		return &sourceFailure{status: http.StatusForbidden, code: "claim_ended", err: err}
	case errors.Is(err, store.ErrClaimCancelRequested):
		return &sourceFailure{status: http.StatusForbidden, code: "claim_cancelled", err: err}
	case errors.Is(err, store.ErrClaimTokenInvalid):
		return &sourceFailure{status: http.StatusUnauthorized, code: "unauthenticated", err: err}
	default:
		return s.sourceInternalFailure(r, "record the source credential", err)
	}
}

func (s *Server) sourceInternalFailure(r *http.Request, what string, err error) *sourceFailure {
	s.logger.Error(what, append(requestLogAttrs(r), "err", err)...)
	return &sourceFailure{status: http.StatusInternalServerError, err: err}
}

func cloudSourceNeedsApp(runID string) string {
	return "Sparkwing Cloud fetches source only through the team's GitHub App, and no installation of the team's " +
		"covers run " + runID + "'s repository (Team > GitHub); stored git credentials serve self-hosted runners only"
}
