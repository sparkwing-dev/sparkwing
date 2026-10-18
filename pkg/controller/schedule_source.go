package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
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
	request.Git.SHA = row.LockedRef
	git, code, err := source.ResolveRevisions(ctx, request.Git, request.Source, "", func(ref string) (string, error) {
		spec := source.Spec{RepoKey: string(request.Git.Repository.Key), RemoteURL: request.Source.Location.RemoteURL, LocalCheckout: request.Source.Location.LocalCheckout}
		options := source.Options{Access: &intakeRevisionAccess{server: s, team: row.Team}, Base: procenv.Base{Ambient: os.Environ(), Masker: s.masker}}
		if row.Where == store.CronWhereLocal {
			options.Base.Local, options.Base.Captured = true, os.Environ()
		}
		return source.ResolveRef(ctx, spec, ref, options)
	})
	if err != nil {
		return request, err
	}
	request.Git, request.Source = git, code

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
		if pipeline := registration.Project.Registry().Find(row.Pipeline); pipeline != nil {
			request.Source.Entrypoint, request.Source.RequestedRef = pipeline.Entrypoint, pipeline.Source
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
