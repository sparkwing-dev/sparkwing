package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// GitCredentialResponse is the credential a runner holding a live claim on a
// run fetches the run's source with. Kind "github_app" carries a short-lived
// App token in Token; the runner hands every kind to git on an inherited pipe
// or a private file, never in its environment.
type GitCredentialResponse struct {
	Kind string `json:"kind"`
	// Host is the host the credential is bound to.
	Host      string `json:"host"`
	Token     string `json:"token,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	// Repository is what an App token reads. ExtraRepositories are the
	// repositories a team owner listed for it: an App token reads them too,
	// and the runner checks out submodules only when there are some.
	Repository        string   `json:"repository,omitempty"`
	ExtraRepositories []string `json:"extra_repositories,omitempty"`
	// Username and Secret are a team credential: the https username and
	// token, or the ssh private key. KnownHosts is the host key an ssh
	// credential pins, the only one the fetch trusts.
	Username   string `json:"username,omitempty"`
	Secret     string `json:"secret,omitempty"`
	KnownHosts string `json:"known_hosts,omitempty"`
}

const (
	gitCredentialGitHubApp = "github_app"
)

// safety: Source fetches use the team's App token or host credential, never the runner's own keys.
// The read-only App token's repository set comes from the owner, not the fetched tree.
func (s *Server) handleRunGitCredential(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	src, ok := s.claimedRunSource(w, r, runID)
	if !ok {
		return
	}
	identity, err := sourceurl.TriggerRepository(src.trigger.RepoURL, src.trigger.TriggerEnv["GITHUB_REPOSITORY"],
		src.trigger.GithubOwner, src.trigger.GithubRepo)
	if err != nil || identity == "" {
		writeNoSourceCredential(w, "run "+runID+" names no repository to fetch")
		return
	}
	host, _, _ := strings.Cut(identity, "/")
	repo, onGitHub := runGitHubRepo(src.trigger)
	var extra []store.GitHubRepo
	if onGitHub {
		if extra, err = ownerExtraRepos(r.Context(), src.tenant, repo); err != nil {
			s.writeInternalError(w, r, "read extra repositories", err)
			return
		}
	}
	if s.githubApp != nil && onGitHub {
		tok, failure := s.runAppToken(r, src, repo, extra)
		if failure == nil {
			writeJSON(w, http.StatusOK, GitCredentialResponse{
				Kind: gitCredentialGitHubApp, Host: "github.com", Token: tok.Token, ExpiresAt: tok.ExpiresAt,
				Repository: tok.Repository, ExtraRepositories: tok.ExtraRepositories,
			})
			return
		}
		if !failure.notCovered {
			failure.write(w)
			return
		}
	}
	s.releaseTeamGitCredential(w, r, src, identity, host, extra)
}

// safety: Only a live same-team claim on the credential's host may receive it.
// The release and audit record commit together.
func (s *Server) releaseTeamGitCredential(w http.ResponseWriter, r *http.Request, src claimedRunSource, identity, host string, extra []store.GitHubRepo) {
	ctx := r.Context()
	runID := src.trigger.ID
	stored, err := src.tenant.GitCredentialForHost(ctx, host)
	if errors.Is(err, store.ErrNotFound) {
		writeNoSourceCredential(w, noSourceCredentialRemedy(identity, host))
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "read git credential", err)
		return
	}
	p, _ := PrincipalFromContext(ctx)
	id := claimIdentity(r)
	if stored.ConfirmedAt == nil {
		writeAuthError(w, http.StatusConflict, authErrorBody{
			Code: "git_credential_unconfirmed", Principal: p.label(),
			Message: "the team's git credential for " + host + " is unusable until a team owner confirms " +
				"its host key " + stored.Fingerprint + " (Team > Git credentials)",
		})
		return
	}
	eligible, err := s.receivesTeamGitCredentials(ctx, src.tenant, id.TokenPrefix)
	if err != nil {
		s.writeInternalError(w, r, "git credential eligibility", err)
		return
	}
	if !eligible {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "git_credential_not_released", Principal: p.label(),
			Message: "the team's git credential for " + host + " is released only to cloud runners and to " +
				"machines a team owner opted in (Team > Machines)",
		})
		return
	}
	if !s.gitCredentialLimit.allow(runID+"\x00"+id.TokenPrefix, gitCredentialsPerMin, time.Now()) {
		setRetryAfter(w, time.Minute)
		writeError(w, http.StatusTooManyRequests, errors.New("this claim asked for its git credential too often"))
		return
	}
	cipher, ok := s.boundCipher(w)
	if !ok {
		return
	}
	released, err := src.tenant.ReleaseGitCredential(ctx, host, store.GitCredentialRelease{
		RunID: runID, Runner: p.label(), TokenPrefix: id.TokenPrefix, Claimant: id,
	}, time.Now())
	if errors.Is(err, store.ErrClaimNotLive) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "claim_required", Principal: p.label(),
			Message: "run " + runID + " is not claimed by this principal",
		})
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeNoSourceCredential(w, noSourceCredentialRemedy(identity, host))
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "release git credential", err)
		return
	}
	secret, err := openSecret(cipher, gitCredentialBinding(src.claimed.Team, released.Host), released.Secret)
	if err != nil {
		s.logger.Error("git credential: open envelope", "team", string(src.claimed.Team), "host", host, "err", err)
		writeError(w, http.StatusInternalServerError, errors.New("the stored git credential did not open"))
		return
	}
	s.logger.Info("git credential released", "team", string(src.claimed.Team), "host", host, "kind", released.Kind,
		"run_id", runID, "principal", p.label(), "token_prefix", id.TokenPrefix)
	writeJSON(w, http.StatusOK, GitCredentialResponse{
		Kind: released.Kind, Host: released.Host, Username: released.Username, Secret: secret,
		KnownHosts: released.KnownHosts, ExtraRepositories: repoSlugs(extra),
	})
}

// safety: Cloud runner claims are metered; machines need the owner's opt-in before receiving team git credentials.
func (s *Server) receivesTeamGitCredentials(ctx context.Context, t *store.Tenant, prefix string) (bool, error) {
	if prefix == "" {
		return false, nil
	}
	metered, err := s.store.TokenMetered(ctx, prefix)
	if err != nil || metered {
		return metered, err
	}
	return t.GitCredentialMachine(ctx, prefix)
}

func noSourceCredentialRemedy(identity, host string) string {
	msg := "no source credential covers " + identity + ": "
	if host == "github.com" {
		slug := strings.TrimPrefix(identity, "github.com/")
		return msg + "install the team's GitHub App on " + slug + " (Team > GitHub), " +
			"or store a git credential for github.com (Team > Git credentials)"
	}
	return msg + "store a git credential for " + host + " (Team > Git credentials): " +
		"an SSH deploy key or an HTTPS token"
}

// bug: No credential differs from a pre-route controller's plain 404.
const noSourceCredentialCode = "no_source_credential"

func writeNoSourceCredential(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusNotFound, authErrorBody{Code: noSourceCredentialCode, Message: message})
}
