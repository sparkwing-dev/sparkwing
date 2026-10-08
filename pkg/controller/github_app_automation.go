package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type githubAppAutomationPipeline struct {
	Pipeline       string   `json:"pipeline"`
	Push           bool     `json:"push"`
	Branches       []string `json:"branches"`
	PullRequest    bool     `json:"pull_request"`
	Actions        []string `json:"actions"`
	BaseBranches   []string `json:"base_branches"`
	ManualOverride bool     `json:"manual_override"`
}

type githubAppAutomationPreview struct {
	Repository     string                        `json:"repository"`
	RepositoryID   int64                         `json:"repository_id"`
	InstallationID int64                         `json:"installation_id"`
	Enabled        bool                          `json:"enabled"`
	DefaultBranch  string                        `json:"default_branch"`
	SourceSHA      string                        `json:"source_sha"`
	Status         string                        `json:"status"`
	Error          string                        `json:"error,omitempty"`
	Pipelines      []githubAppAutomationPipeline `json:"pipelines"`
}

func validateAutomationPatterns(patterns []string) error {
	if len(patterns) > 10 {
		return errors.New("at most 10 branch patterns are supported")
	}
	for _, pattern := range patterns {
		if pattern == "" || len(pattern) > 128 {
			return errors.New("branch patterns must be 1 to 128 bytes")
		}
		if strings.Contains(pattern, "**") {
			return errors.New("recursive ** branch patterns are unsupported; use explicit branches or a single *")
		}
		if strings.HasPrefix(pattern, "!") {
			return errors.New("negative branch patterns are unsupported; list the branches to include")
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("invalid branch pattern %q: %w", pattern, err)
		}
	}
	return nil
}

func automationDeclarations(content []byte) ([]githubAppAutomationPipeline, string, error) {
	cfg, err := projectconfig.Parse(content)
	if err != nil {
		return nil, "invalid", err
	}
	out := []githubAppAutomationPipeline{}
	for _, pipeline := range cfg.Pipelines {
		decl := githubAppAutomationPipeline{Pipeline: pipeline.Name, Branches: []string{}, Actions: []string{}, BaseBranches: []string{}}
		if push := pipeline.On.Push; push != nil {
			if len(push.Paths) != 0 {
				return nil, "unsupported", fmt.Errorf("pipeline %s: on.push.paths is unsupported by repository automation", pipeline.Name)
			}
			if err := validateAutomationPatterns(push.Branches); err != nil {
				return nil, "unsupported", fmt.Errorf("pipeline %s: on.push.branches: %w", pipeline.Name, err)
			}
			decl.Push = true
			decl.Branches = append(decl.Branches, push.Branches...)
		}
		if pr := pipeline.On.PullRequest; pr != nil {
			if err := validateAutomationPatterns(pr.Branches); err != nil {
				return nil, "unsupported", fmt.Errorf("pipeline %s: on.pull_request.branches: %w", pipeline.Name, err)
			}
			decl.PullRequest = true
			decl.BaseBranches = append(decl.BaseBranches, pr.Branches...)
			decl.Actions = append(decl.Actions, pr.Actions...)
			if len(decl.Actions) == 0 {
				decl.Actions = []string{"opened", "synchronize", "reopened"}
			}
			for _, action := range decl.Actions {
				if !slices.Contains([]string{"opened", "synchronize", "reopened", "closed", "ready_for_review"}, action) {
					return nil, "unsupported", fmt.Errorf("pipeline %s: on.pull_request.actions %q is unsupported by repository automation", pipeline.Name, action)
				}
			}
		}
		if decl.Push || decl.PullRequest {
			out = append(out, decl)
		}
	}
	return out, "ready", nil
}

func (s *Server) githubAppAutomationPreview(ctx context.Context, tenant *store.Tenant, in store.GitHubAppInstallation, repo store.GitHubRepo) (githubAppAutomationPreview, error) {
	out := githubAppAutomationPreview{Repository: repo.Slug(), RepositoryID: repo.ID, InstallationID: in.InstallationID, Pipelines: []githubAppAutomationPipeline{}}
	if _, err := tenant.GitHubAppAutomation(ctx, in.InstallationID, repo.ID); err == nil {
		out.Enabled = true
	} else if !errors.Is(err, store.ErrNotFound) {
		return out, err
	}
	snapshot, err := s.githubApp.client.FileAtDefaultHead(ctx, in.InstallationID, repo.Owner, repo.Name, ".sparkwing/"+projectconfig.Filename)
	if err != nil {
		out.Status = "unavailable"
		out.Error = "GitHub could not read the repository configuration; retry discovery"
		return out, nil
	}
	if snapshot.RepositoryID != repo.ID {
		return out, errors.New("GitHub repository identity changed while reading configuration")
	}
	out.DefaultBranch, out.SourceSHA = snapshot.DefaultBranch, snapshot.HeadSHA
	if !snapshot.Found {
		out.Status = "missing"
		out.Error = "Add .sparkwing/sparkwing.yaml to the default branch with on.push or on.pull_request declarations"
		return out, nil
	}
	decls, status, err := automationDeclarations(snapshot.Content)
	out.Status = status
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	manual, err := tenant.GitHubAppTriggersFor(ctx, in.InstallationID, repo.ID)
	if err != nil {
		return out, err
	}
	for i := range decls {
		for _, sub := range manual {
			if sub.Pipeline == decls[i].Pipeline {
				decls[i].ManualOverride = true
				break
			}
		}
	}
	out.Pipelines = decls
	return out, nil
}

func (s *Server) resolveAutomationRepository(ctx context.Context, tenant *store.Tenant, slug string) (store.GitHubAppInstallation, store.GitHubRepo, error) {
	in, current, err := s.resolveGitHubAppRepository(ctx, tenant, slug)
	if err != nil {
		return in, store.GitHubRepo{}, err
	}
	repo, _ := store.ParseGitHubRepo(current.FullName)
	repo.ID = current.ID
	return in, repo, nil
}

func (s *Server) handleGitHubAppAutomation(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	role := store.RoleReader
	if r.Method != http.MethodGet {
		role = store.RoleOwner
	}
	p, tenant, ok := s.teamMember(w, r, role)
	if !ok {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid automation query"))
		return
	}
	queryKey := "repository"
	if r.Method == http.MethodDelete {
		queryKey = "repository_id"
	}
	for key, values := range query {
		if key != queryKey || len(values) != 1 || (r.Method == http.MethodPut) {
			writeError(w, http.StatusBadRequest, errors.New("invalid automation query"))
			return
		}
	}
	if r.Method == http.MethodDelete {
		id, err := strconv.ParseInt(r.URL.Query().Get("repository_id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("repository_id must be a positive integer"))
			return
		}
		if err := tenant.DeleteGitHubAppAutomation(r.Context(), id); err != nil {
			s.writeInternalError(w, r, "revoke repository automation", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	slug := r.URL.Query().Get("repository")
	if r.Method == http.MethodPut {
		var req struct {
			Repository string `json:"repository"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		slug = req.Repository
	} else if slug == "" {
		if r.URL.RawQuery != "" {
			writeError(w, http.StatusBadRequest, errors.New("repository must be owner/name"))
			return
		}
		list, err := tenant.GitHubAppAutomations(r.Context())
		if err != nil {
			s.writeInternalError(w, r, "list repository automation", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"automation": list})
		return
	}
	in, repo, err := s.resolveAutomationRepository(r.Context(), tenant, slug)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, store.ErrInvalidInput) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
			err = errors.New("no installation this team holds covers " + slug)
		}
		writeError(w, status, err)
		return
	}
	out, err := s.githubAppAutomationPreview(r.Context(), tenant, in, repo)
	if err != nil {
		s.writeInternalError(w, r, "discover repository automation", err)
		return
	}
	if r.Method == http.MethodPut {
		if out.Status != "ready" {
			writeJSON(w, http.StatusUnprocessableEntity, out)
			return
		}
		if err := tenant.PutGitHubAppAutomation(r.Context(), store.GitHubAppAutomation{RepositoryID: repo.ID, Repository: repo.Slug(), InstallationID: in.InstallationID, EnabledBy: p.AccountID}, time.Now()); err != nil {
			s.writeInternalError(w, r, "enable repository automation", err)
			return
		}
		out.Enabled = true
	}
	writeJSON(w, http.StatusOK, out)
}

func automationSubscribes(decl githubAppAutomationPipeline, event, action string, intake githubAppIntake) bool {
	switch event {
	case "push":
		return decl.Push && intake.env["GITHUB_REF_TYPE"] == "branch" && githubAppBranchMatches(decl.Branches, intake.branch)
	case "pull_request":
		return decl.PullRequest && slices.Contains(decl.Actions, action) && githubAppBranchMatches(decl.BaseBranches, intake.env[sparkwing.EnvPRBaseRef])
	}
	return false
}
