package controller

import (
	"context"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githuboidc"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: GitHub's terms allow Actions only for the software project in the repository the
// workflow runs in, so a job's credential reaches only runs of that one repository.

// GitHubActionsLabel identifies a GitHub Actions runner observation.
const GitHubActionsLabel = "github-actions"

// safety: a job never outlives six hours and a node that outlives the
// credential fails, so the credential is short and the workflow's timeout
// matches it.
const githubRunnerCredentialTTL = time.Hour

// safety: pull_request runs a contributor's code, and pull_request_target and workflow_run hand
// the base repository's privileges to input a fork controls, so those jobs get no credential.
var githubRunnerEvents = map[string]bool{"push": true, "workflow_dispatch": true, "schedule": true}

func githubCommit(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	for _, c := range sha {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

type githubRunnerConfig struct {
	registry func(context.Context, store.Team) (GitHubRunnerRegistry, error)
	once     sync.Once
	override *githuboidc.Config
	verifier *githuboidc.Verifier
	lookup   func(context.Context, store.Team, int64) (store.GitHubRunnerBinding, error)
}

// WithGitHubRunners trusts cfg's issuer for the GitHub Actions exchange
// instead of GitHub's production issuer with the external URL as audience.
func (s *Server) WithGitHubRunners(cfg githuboidc.Config) *Server {
	s.githubRunners.override = &cfg
	return s
}

// safety: the audience is the external URL, so a job's token for any other
// service cannot be exchanged here; without one the exchange stays off.
func (s *Server) githubVerifier() *githuboidc.Verifier {
	s.githubRunners.once.Do(func() {
		switch {
		case s.githubRunners.override != nil:
			s.githubRunners.verifier = githuboidc.New(*s.githubRunners.override)
		case s.externalURL != "":
			s.githubRunners.verifier = githuboidc.New(githuboidc.GitHub(s.externalURL))
		}
	})
	return s.githubRunners.verifier
}

type githubExchangeReq struct {
	IDToken string `json:"id_token"`
	Team    string `json:"team"`
}

type githubExchangeResp struct {
	Token      string   `json:"token"`
	Team       string   `json:"team"`
	Repository string   `json:"repository"`
	ExpiresAt  int64    `json:"expires_at"`
	Labels     []string `json:"labels"`
}

type githubBindingReq struct {
	Repository        string `json:"repository"`
	RepositoryID      int64  `json:"repository_id"`
	RepositoryOwnerID int64  `json:"repository_owner_id"`
}

type githubBindingJSON struct {
	Repository        string `json:"repository"`
	RepositoryID      int64  `json:"repository_id"`
	RepositoryOwnerID int64  `json:"repository_owner_id"`
	CreatedBy         string `json:"created_by"`
	CreatedAt         int64  `json:"created_at"`
}

type githubBindingsResp struct {
	Bindings []githubBindingJSON `json:"bindings"`
	Workflow string              `json:"workflow"`
}

func githubBindingOut(b store.GitHubRunnerBinding) githubBindingJSON {
	return githubBindingJSON{
		Repository: b.Repository, RepositoryID: b.RepositoryID, RepositoryOwnerID: b.RepositoryOwnerID,
		CreatedBy: b.CreatedBy, CreatedAt: b.CreatedAt.Unix(),
	}
}

// GitHubRunnerWorkflow renders the workflow a bound repository commits so its
// pushes run the team's work for that repository on GitHub Actions minutes.
func GitHubRunnerWorkflow(controllerURL, team string) string {
	return `# Runs Sparkwing work for this repository on this repository's GitHub
# Actions minutes. The controller hands the job only triggers and nodes for this
# repository, and the job exits once the queue has been empty for --idle-exit.
name: sparkwing
on:
  push:
  workflow_dispatch:
permissions:
  id-token: write
  contents: read
jobs:
  sparkwing:
    runs-on: ubuntu-latest
    # The runner credential lives one hour, so the job does too.
    timeout-minutes: 60
    steps:
      - uses: actions/checkout@v4
      - name: Install sparkwing-runner
        run: |
          base=https://github.com/sparkwing-dev/sparkwing/releases/latest/download
          curl -fsSLO "$base/sparkwing-runner-linux-amd64"
          curl -fsSLO "$base/SHA256SUMS"
          grep ' sparkwing-runner-linux-amd64$' SHA256SUMS | sha256sum -c -
          install -m 0755 sparkwing-runner-linux-amd64 "$RUNNER_TEMP/sparkwing-runner"
      - name: Run this repository's Sparkwing work
        run: |
          "$RUNNER_TEMP/sparkwing-runner" runner --github-actions \
            --team ` + team + ` \
            --controller ` + controllerURL + ` \
            --idle-exit 2m
`
}
