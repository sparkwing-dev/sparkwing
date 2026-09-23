package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githuboidc"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A GitHub Actions job can run a team's work on the job's own minutes. GitHub's
// terms allow Actions to be used for the production, testing, deployment or
// publication of the software project in the repository the workflow runs in,
// so a job's credential reaches only runs of that one repository, and the
// controller enforces that on every request the credential makes.

// GitHubActionsLabel is the runner label a GitHub Actions job advertises. It
// is not the local label, so the placement hold keeps a team's own machines
// first and a job picks a node up only after the hold.
const GitHubActionsLabel = "github-actions"

// safety: a job never outlives six hours and a node that outlives the
// credential fails, so the credential is short and the workflow's timeout
// matches it.
const githubRunnerCredentialTTL = time.Hour

// githubRunnerEvents are the workflow events whose job runs code the
// repository's owners pushed. pull_request runs a contributor's code, and
// pull_request_target and workflow_run hand the base repository's
// privileges to input a fork controls, so those jobs get no credential.
var githubRunnerEvents = map[string]bool{"push": true, "workflow_dispatch": true, "schedule": true}

// githubCommit reports whether sha is a full commit id: 40 hex digits, or 64
// in a SHA-256 repository.
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
	once     sync.Once
	override *githuboidc.Config
	verifier *githuboidc.Verifier
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

var errNoGitHubBinding = errors.New("this team has not bound the job's repository to GitHub Actions runners")

func (s *Server) handleGitHubRunnerExchange(w http.ResponseWriter, r *http.Request) {
	verifier := s.githubVerifier()
	if verifier == nil {
		writeError(w, http.StatusNotFound, errors.New("GitHub Actions runners need the controller's external URL"))
		return
	}
	var req githubExchangeReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	claims, err := verifier.Verify(r.Context(), req.IDToken)
	if errors.Is(err, githuboidc.ErrRejected) {
		s.logger.Info("github runner exchange rejected", "reason", err.Error())
		writeAuthError(w, http.StatusUnauthorized, authErrorBody{Code: "unauthenticated", Message: err.Error()})
		return
	}
	if err != nil {
		s.logger.Warn("github runner exchange could not verify", "error", err.Error())
		writeError(w, http.StatusBadGateway, errors.New("GitHub's signing keys could not be read"))
		return
	}
	repo, ok := store.ParseGitHubRepo(claims.Repository)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("the token names no owner/name repository"))
		return
	}
	branch, isBranch := strings.CutPrefix(claims.Ref, "refs/heads/")
	var refusal string
	switch {
	case !githubRunnerEvents[claims.EventName]:
		refusal = "a " + strconv.Quote(claims.EventName) + " job gets no runner credential; only push, workflow_dispatch and schedule jobs do"
	case !isBranch || branch == "":
		refusal = "the job ran for " + strconv.Quote(claims.Ref) + "; a runner credential binds only to a branch"
	case !githubCommit(claims.SHA):
		refusal = "the token names no commit"
	}
	if refusal != "" {
		s.logger.Info("github runner exchange refused",
			"repository", claims.Repository, "event", claims.EventName, "ref", claims.Ref, "reason", refusal)
		writeAuthError(w, http.StatusForbidden, authErrorBody{Code: "forbidden", Message: refusal})
		return
	}
	// safety: consent is two-sided: the workflow names the team, and an
	// owner of that team bound this repository id. A team binding a
	// repository it does not control gets nothing, because that
	// repository's workflows never name it.
	team := store.NormalizeTeam(store.Team(req.Team))
	t, err := s.store.ForTeam(r.Context(), team)
	if err != nil && !errors.Is(err, store.ErrUnknownTeam) && !errors.Is(err, store.ErrNoTeam) {
		s.writeInternalError(w, r, "github runner team", err)
		return
	}
	var binding store.GitHubRunnerBinding
	if err == nil {
		binding, err = t.GitHubRunnerBinding(r.Context(), claims.RepositoryID)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrUnknownTeam) && !errors.Is(err, store.ErrNoTeam) {
		s.writeInternalError(w, r, "github runner binding", err)
		return
	}
	// safety: a transferred repository keeps its id, so the owner id pinned at
	// binding time is what stops its new owner inheriting the team's work.
	if err != nil || binding.RepositoryOwnerID != claims.RepositoryOwnerID {
		s.logger.Info("github runner exchange refused",
			"team", string(team), "repository", claims.Repository, "repository_id", claims.RepositoryID,
			"reason", "no binding")
		writeAuthError(w, http.StatusForbidden, authErrorBody{Code: "forbidden", Message: errNoGitHubBinding.Error()})
		return
	}
	now := time.Now().UTC()
	principal := store.GitHubRunnerPrincipalPrefix + strconv.FormatInt(claims.RepositoryID, 10) + ":" + repo.Slug()
	raw, tok, err := t.MintGitHubRunnerCredential(r.Context(), principal,
		store.GitHubRunnerPush{Branch: branch, SHA: claims.SHA}, runnerTokenScopes, githubRunnerCredentialTTL, now)
	if errors.Is(err, store.ErrGitHubRunnerCredentialLimit) {
		setRetryAfter(w, time.Minute)
		writeError(w, http.StatusTooManyRequests, errors.New(
			"this team already has "+strconv.Itoa(store.MaxGitHubRunnerCredentials)+" live GitHub Actions runner credentials"))
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "mint github runner credential", err)
		return
	}
	s.logger.Info("github runner credential minted",
		"team", string(team), "prefix", tok.Prefix, "repository", claims.Repository,
		"repository_id", claims.RepositoryID, "ref", claims.Ref, "sha", claims.SHA, "event", claims.EventName,
		"workflow_ref", claims.WorkflowRef, "github_run_id", claims.RunID)
	writeJSON(w, http.StatusCreated, githubExchangeResp{
		Token: raw, Team: string(team), Repository: repo.Slug(),
		ExpiresAt: tok.ExpiresAt.Unix(), Labels: []string{GitHubActionsLabel},
	})
}

// githubRunnerScope reports whether p is a GitHub Actions job's credential
// and, when it is, the only work it may reach. ok is false for a credential
// whose principal does not parse, which the fence refuses outright.
func githubRunnerScope(p *Principal) (scope store.GitHubRunnerScope, isGitHub, ok bool) {
	if p == nil || !strings.HasPrefix(p.Name, store.GitHubRunnerPrincipalPrefix) {
		return store.GitHubRunnerScope{}, false, false
	}
	rest := strings.TrimPrefix(p.Name, store.GitHubRunnerPrincipalPrefix)
	id, slug, found := strings.Cut(rest, ":")
	if _, err := strconv.ParseInt(id, 10, 64); !found || err != nil {
		return store.GitHubRunnerScope{}, true, false
	}
	repo, parsed := store.ParseGitHubRepo(slug)
	if !parsed || p.Team == "" {
		return store.GitHubRunnerScope{}, true, false
	}
	return store.GitHubRunnerScope{Team: p.Team, Repo: repo}, true, true
}

// githubRunnerFence holds a GitHub Actions job's credential to its own
// repository's work. Every route the credential may use is listed; any
// other answers 403, so a route added later is closed to it until someone
// decides how it is confined.
func (s *Server) githubRunnerFence(next http.Handler) http.Handler {
	fence := s.githubFenceMux(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFromContext(r.Context())
		scope, isGitHub, ok := githubRunnerScope(p)
		if !isGitHub {
			next.ServeHTTP(w, r)
			return
		}
		if !ok {
			writeGitHubFenceRefusal(w, p, "this credential does not name the repository it is bound to")
			return
		}
		t, err := s.store.ForTeam(r.Context(), scope.Team)
		if err == nil {
			scope.Push, err = t.GitHubRunnerCredentialPush(r.Context(), p.TokenPrefix)
		}
		if errors.Is(err, store.ErrNotFound) {
			writeGitHubFenceRefusal(w, p, "this credential names no push it was minted for")
			return
		}
		if err != nil {
			s.writeInternalError(w, r, "github runner push", err)
			return
		}
		fence.ServeHTTP(w, r.WithContext(store.WithGitHubRunnerScope(r.Context(), scope)))
	})
}

func (s *Server) githubFenceMux(next http.Handler) *http.ServeMux {
	pass := next
	byRun := s.githubFenceRun(next)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/nodes/claim", s.githubFenceQueueClaim(next))
	mux.Handle("POST /api/v1/triggers/claim", pass)
	// safety: a non-admin run create is bound to the caller's live claim
	// on the run's trigger, which the scoped trigger claim already confines.
	mux.Handle("POST /api/v1/runs", pass)
	mux.Handle("/api/v1/runs/{id}", byRun)
	mux.Handle("/api/v1/runs/{id}/{rest...}", byRun)
	mux.Handle("/api/v1/triggers/{id}", byRun)
	mux.Handle("/api/v1/triggers/{id}/{rest...}", byRun)
	// safety: each of these binds to a live claim on a named run or
	// pipeline, and this credential holds claims only on its own
	// repository's runs.
	mux.Handle("/api/v1/concurrency/{key}/{rest...}", pass)
	mux.Handle("/api/v1/pipelines/{name}/profile", pass)
	mux.Handle("/api/v1/pipelines/{name}/profile/{rest...}", pass)
	mux.Handle("GET /api/v1/secrets/{name}", pass)
	mux.Handle("POST /api/v1/agents/{name}/heartbeat", pass)
	mux.Handle("GET /api/v1/auth/whoami", pass)
	mux.Handle("GET /api/v1/services", pass)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFromContext(r.Context())
		writeGitHubFenceRefusal(w, p, "a GitHub Actions runner credential cannot use "+r.Method+" "+r.URL.Path)
	})
	return mux
}

func (s *Server) githubFenceRun(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFromContext(r.Context())
		scope, _ := store.GitHubRunnerScopeFrom(r.Context())
		ok, err := s.store.GitHubRunnerAdmits(r.Context(), scope, r.PathValue("id"))
		if err != nil {
			s.writeInternalError(w, r, "github runner fence", err)
			return
		}
		if !ok {
			writeGitHubFenceRefusal(w, p, "run "+r.PathValue("id")+" is not work of "+scope.Repo.Slug())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// safety: an executor offer names its run in the body and is placed by the
// offer round rather than the scoped queue scan, so this credential may only
// take the queue's next node.
func (s *Server) githubFenceQueueClaim(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
		_ = r.Body.Close()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var probe struct {
			ExecutorName string `json:"executor_name"`
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &probe); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		if probe.ExecutorName != "" {
			p, _ := PrincipalFromContext(r.Context())
			writeGitHubFenceRefusal(w, p, "a GitHub Actions runner credential cannot answer an executor offer")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

func writeGitHubFenceRefusal(w http.ResponseWriter, p *Principal, msg string) {
	writeAuthError(w, http.StatusForbidden, authErrorBody{Code: "forbidden", Principal: p.label(), Message: msg})
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
	// Workflow is the file to commit as .github/workflows/sparkwing.yaml in
	// a bound repository.
	Workflow string `json:"workflow"`
}

func githubBindingOut(b store.GitHubRunnerBinding) githubBindingJSON {
	return githubBindingJSON{
		Repository: b.Repository, RepositoryID: b.RepositoryID, RepositoryOwnerID: b.RepositoryOwnerID,
		CreatedBy: b.CreatedBy, CreatedAt: b.CreatedAt.Unix(),
	}
}

func (s *Server) handleListGitHubRunnerBindings(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	bindings, err := t.GitHubRunnerBindings(r.Context())
	if err != nil {
		s.writeInternalError(w, r, "list github runner bindings", err)
		return
	}
	out := githubBindingsResp{Bindings: []githubBindingJSON{}, Workflow: GitHubRunnerWorkflow(s.controllerURL(r), string(p.Team))}
	for _, b := range bindings {
		out.Bindings = append(out.Bindings, githubBindingOut(b))
	}
	writeJSON(w, http.StatusOK, out)
}

// safety: only an owner binds, because a binding lets a repository's
// workflows run the team's work for that repository.
func (s *Server) handleAddGitHubRunnerBinding(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req githubBindingReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	b, err := t.AddGitHubRunnerBinding(r.Context(), store.GitHubRunnerBinding{
		Repository: req.Repository, RepositoryID: req.RepositoryID, RepositoryOwnerID: req.RepositoryOwnerID,
		CreatedBy: p.AccountID,
	}, time.Now())
	if errors.Is(err, store.ErrAlreadyBound) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeIdentityError(w, s, r, "add github runner binding", err)
		return
	}
	s.logger.Info("github runner binding added", "team", string(p.Team), "repository", b.Repository,
		"repository_id", b.RepositoryID, "by", p.AccountID)
	writeJSON(w, http.StatusCreated, githubBindingsResp{
		Bindings: []githubBindingJSON{githubBindingOut(b)},
		Workflow: GitHubRunnerWorkflow(s.controllerURL(r), string(p.Team)),
	})
}

func (s *Server) handleRemoveGitHubRunnerBinding(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("repository_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("repository_id is GitHub's numeric repository id"))
		return
	}
	revoked, err := t.RemoveGitHubRunnerBinding(r.Context(), id, time.Now())
	if err != nil {
		writeIdentityError(w, s, r, "remove github runner binding", err)
		return
	}
	for _, prefix := range revoked {
		s.auth.Invalidate(prefix)
	}
	s.logger.Info("github runner binding removed", "team", string(p.Team), "repository_id", id,
		"revoked_credentials", len(revoked), "by", p.AccountID)
	w.WriteHeader(http.StatusNoContent)
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
