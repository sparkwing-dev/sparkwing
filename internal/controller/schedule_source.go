package controller

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// ResolveScheduleSource derives checkout identity from stored schedule intent.
func (s *Server) ResolveScheduleSource(ctx context.Context, row store.CronSchedule, request runspec.Request) (runspec.Request, error) {
	if row.GitHubInstallationID != 0 && s.githubApp == nil {
		return request, s.githubAppUnavailable()
	}
	request.Version = 1
	request.Pipeline = row.Pipeline
	request.Mode = runspec.IntakeExecute
	request.Origin = runspec.OriginSchedule
	request.RequestedArgs = row.Effective().Args
	if row.Where != store.CronWhereLocal {
		request.Git.Ref = "refs/heads/" + strings.TrimPrefix(row.GitBranch, "refs/heads/")
	}
	if row.Where == store.CronWhereLocal {
		if !filepath.IsAbs(row.RepoPath) || request.Source.Location.LocalCheckout != row.RepoPath || request.Env.Digest == "" {
			return request, errors.New("local schedule inputs are unavailable")
		}
		repository, err := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: row.RepoPath})
		if err != nil {
			return request, err
		}
		request.Git.Repository = repository
	} else {
		request.Source.Location = runspec.SourceLocation{RemoteURL: row.RepoPath, PipelineDir: ".sparkwing"}
		if err := s.resolveIntakeSource(ctx, string(row.Team), &request); err != nil {
			return request, err
		}
	}
	request.Git.SHA = row.LockedRef
	var read func(context.Context, string, string) ([]byte, bool, error)
	if row.GitHubInstallationID != 0 && s.githubApp != nil {
		display, err := sourceurl.Identity(row.RepoPath)
		if err != nil {
			return request, err
		}
		_, slug, _ := strings.Cut(display, "/")
		repo, valid := store.ParseGitHubRepo(strings.TrimSuffix(slug, ".git"))
		if !valid {
			return request, errors.New("schedule GitHub repository is invalid")
		}
		read = func(ctx context.Context, sha, name string) ([]byte, bool, error) {
			return s.githubApp.client.FileAtCommit(ctx, row.GitHubInstallationID, row.GitHubRepositoryID, repo.Owner, repo.Name, sha, name)
		}
	}
	git, code, err := s.selectIntakeSource(ctx, row.Team, request, read)
	if err != nil {
		return request, err
	}
	request.Git, request.Source = git, code
	if row.LockedRef == "" && row.Where != store.CronWhereLocal && s.githubApp != nil && strings.HasPrefix(string(request.Git.Repository.Key), "gh:") {
		branch, err := s.githubDefaultBranch(ctx, row)
		if err != nil {
			return request, err
		}
		request.Git.DefaultBranch = runspec.Optional[string]{Set: true, Value: branch}
	}

	return request, nil
}

// githubDefaultBranch asks GitHub, through the team's installation covering the
// schedule's repository, which branch the repository serves as its head.
func (s *Server) githubDefaultBranch(ctx context.Context, row store.CronSchedule) (string, error) {
	tenant, err := s.tenantForTeam(ctx, row.Team)
	if err != nil {
		return "", err
	}
	display, err := sourceurl.Identity(row.RepoPath)
	if err != nil {
		return "", err
	}
	_, slug, _ := strings.Cut(display, "/")
	installation, repository, err := s.resolveGitHubAppRepository(ctx, tenant, strings.TrimSuffix(slug, ".git"))
	if err != nil {
		return "", err
	}
	token, err := s.githubApp.client.InstallationTokenByIDs(ctx, installation.InstallationID, []int64{repository.ID}, map[string]string{"metadata": "read"})
	if err != nil {
		return "", err
	}
	repo, err := s.githubApp.client.RepositoryByID(ctx, token.Token, repository.ID)
	if err == nil && repo.DefaultBranch == "" {
		err = errors.New("GitHub reported no default branch")
	}
	return repo.DefaultBranch, err
}

func (s *Server) commitLocalSchedules(ctx context.Context, a capability.Authorized, root string, rows []store.CronSchedule, registration crons.LocalRegistration, replace bool) (crons.ArmReport, error) {
	tenant, err := s.tenantForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return crons.ArmReport{}, err
	}
	var requests []runspec.Request
	for _, row := range rows {
		request := runspec.Request{Env: registration.Environment, EnvironmentSource: runspec.EnvironmentCaptured, Source: runspec.Source{Location: runspec.SourceLocation{LocalCheckout: root, PipelineDir: ".sparkwing"}}}
		if pipeline := registration.Project.Registry().Find(row.Pipeline); pipeline != nil {
			request.Source.Entrypoint = pipeline.Entrypoint
		}
		request, err = s.ResolveScheduleSource(ctx, row, request)
		if err != nil {
			return crons.ArmReport{}, err
		}
		requests = append(requests, request)
	}
	ids, withdrawn, err := tenant.ReplaceAuthorizedLocalSchedules(ctx, a, root, rows, requests, replace)
	if err != nil {
		return crons.ArmReport{}, err
	}
	report := crons.ArmReport{Withdrawals: withdrawn, Withdrawn: len(withdrawn)}
	for _, id := range ids {
		row, err := tenant.GetCronSchedule(ctx, id)
		if err != nil {
			return crons.ArmReport{}, err
		}
		report.Schedules = append(report.Schedules, row)
	}
	return report, nil
}

var _ store.ScheduleSourcePolicy = (*Server)(nil)

var (
	_ ScheduleWriter     = (*store.Tenant)(nil)
	_ ScheduledRunWriter = (*store.Tenant)(nil)
	_ ConcurrencyStore   = (*store.Tenant)(nil)
)
