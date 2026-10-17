package controller

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ResolveScheduleSource derives checkout identity from stored schedule intent.
func (s *Server) ResolveScheduleSource(ctx context.Context, row store.CronSchedule, request runspec.Request) (runspec.Request, error) {
	request.Version = 1
	request.Pipeline = row.Pipeline
	request.Mode = runspec.IntakeExecute
	request.Origin = runspec.OriginSchedule
	request.Dispatch = runspec.DispatchController
	request.RequestedArgs = row.Effective().Args
	if row.Where != store.CronWhereLocal {
		request.Source.Ref = row.GitBranch
		request.Git.Ref = row.GitBranch
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
	if row.LockedRef != "" {
		request.Git.SHA = row.LockedRef
	} else {
		var args []string
		if row.Where == store.CronWhereLocal {
			args = []string{"-C", row.RepoPath, "rev-parse", "--verify", "HEAD^{commit}"}
		} else {
			if err := validateGitBranchName(row.GitBranch); err != nil {
				return request, err
			}
			if err := sourceurl.CheckResolvedHost(ctx, row.RepoPath, net.DefaultResolver.LookupIPAddr); err != nil {
				return request, err
			}
			args = []string{"-c", "credential.helper=", "-c", "http.followRedirects=false", "ls-remote", "--exit-code", "--", row.RepoPath, "refs/heads/" + row.GitBranch}
		}
		environment, err := procenv.Build(procenv.Git, procenv.Base{Local: true, Captured: os.Environ()}, procenv.Runtime{})
		if err != nil {
			return request, err
		}
		environment = append(environment, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:ssh", "GIT_SSH_COMMAND=ssh -F "+os.DevNull+" -o BatchMode=yes -o IdentityAgent=none -o IdentityFile=none -o StrictHostKeyChecking=yes -o ForwardAgent=no -o ClearAllForwardings=yes")
		command := exec.CommandContext(ctx, "git", args...)
		command.Env = environment
		body, err := command.Output()
		if err != nil {
			return request, errors.New("resolve schedule source revision failed")
		}
		fields := strings.Fields(string(body))
		if len(fields) == 0 {
			return request, errors.New("schedule revision is unavailable")
		}
		request.Git.SHA = fields[0]
	}
	if err := validateGitSHA(request.Git.SHA); err != nil {
		return request, err
	}
	return request, nil
}

func (s *Server) commitLocalSchedules(ctx context.Context, a capability.Authorized, root string, rows []store.CronSchedule, registration crons.LocalRegistration, replace bool) (crons.ArmReport, error) {
	tenant, err := s.tenantForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return crons.ArmReport{}, err
	}
	var requests []runspec.Request
	for _, row := range rows {
		request := runspec.Request{Env: registration.Environment, EnvironmentSource: runspec.EnvironmentCaptured, Source: runspec.Source{Location: runspec.SourceLocation{LocalCheckout: root, PipelineDir: ".sparkwing"}}}
		for _, pipeline := range registration.Project.Registry().Pipelines {
			if pipeline.Name == row.Pipeline {
				request.Source.Entrypoint = pipeline.Entrypoint
				request.Source.Ref = pipeline.Source
			}
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
