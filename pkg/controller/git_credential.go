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
	// Repository and ExtraRepositories are what an App token reads.
	Repository        string   `json:"repository,omitempty"`
	ExtraRepositories []string `json:"extra_repositories,omitempty"`
	// Username and Secret are a team credential: the https username and
	// token, or the ssh private key. KnownHosts is the host key an ssh
	// credential pins, the only one the fetch trusts.
	Username   string `json:"username,omitempty"`
	Secret     string `json:"secret,omitempty"`
	KnownHosts string `json:"known_hosts,omitempty"`
}

// Kinds of [GitCredentialResponse].
const (
	gitCredentialGitHubApp = "github_app"
)

type gitCredentialReq struct {
	// ExtraRepos is the pipeline's declared source.extra_repos, as
	// owner/name slugs.
	ExtraRepos []string `json:"extra_repos,omitempty"`
}

// handleRunGitCredential resolves the one credential a run's source is
// fetched with, in a fixed order: the team's GitHub App token when an
// installation the team holds covers the repository, else the git credential
// the team stored for the repository's host, else a refusal that names the
// remedy. A runner never falls back to credentials of its own.
func (s *Server) handleRunGitCredential(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	var req gitCredentialReq
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
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
	if s.githubApp != nil {
		if repo, ok := runGitHubRepo(src.trigger); ok {
			tok, failure := s.runAppToken(r, src, repo, nil)
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
	}
	s.releaseTeamGitCredential(w, r, src, identity, host)
}

// safety: a team credential leaves the controller only for a runner that
// holds a live claim on a run of the credential's team (claimedRunSource),
// whose source is on the credential's host (the lookup is by that host), and
// that is a cloud runner or a machine the team's owner opted in. The release
// and its audit row are one transaction, so a deleted credential is never
// released and none is released unrecorded.
func (s *Server) releaseTeamGitCredential(w http.ResponseWriter, r *http.Request, src claimedRunSource, identity, host string) {
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
		RunID: runID, Runner: p.label(), TokenPrefix: id.TokenPrefix,
	}, time.Now())
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
		KnownHosts: released.KnownHosts,
	})
}

// receivesTeamGitCredentials reports whether the runner token prefix is a
// cloud runner, whose claims the ledger meters, or a machine the team's owner
// opted in.
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

// noSourceCredentialCode is the error code a runner reads as "nothing to
// fetch with", apart from the plain 404 of a controller without the route.
const noSourceCredentialCode = "no_source_credential"

func writeNoSourceCredential(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusNotFound, authErrorBody{Code: noSourceCredentialCode, Message: message})
}
