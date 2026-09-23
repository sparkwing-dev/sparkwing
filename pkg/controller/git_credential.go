package controller

import (
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
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
// installation the team holds covers the repository, and otherwise a refusal
// that names the remedy. A runner never falls back to credentials of its own.
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
	writeNoSourceCredential(w, noSourceCredentialRemedy(identity, host))
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
