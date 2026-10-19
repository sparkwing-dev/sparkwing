package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a personal machine's repository access comes only from the App installation, never from its claim, and a
// failed check records a denial, so the machine stays ineligible until the next check.
func (s *Server) refreshPersonalRepoAccess(ctx context.Context, c capability.Capability) {
	if s.store == nil || s.githubApp == nil || c.Subject.Kind != capability.SubjectMachine || c.Binding.Kind != capability.BindingNone || c.Parent != nil {
		return
	}
	tenant, err := s.store.ForTeam(ctx, store.Team(c.Team))
	if err != nil {
		return
	}
	needs, err := tenant.PersonalRepoAccessNeeds(ctx, c.Prefix)
	if err != nil {
		s.logger.Warn("personal machine access check failed", "err", err)
		return
	}
	access := map[string]bool{}
	for _, need := range needs {
		repo, ok := store.ParseGitHubRepo(need.Repository)
		if !ok {
			continue
		}
		access[need.Repository] = false
		installation, covered, err := s.teamInstallationFor(ctx, tenant, repo)
		if err == nil && covered {
			var readable map[string]bool
			readable, err = s.githubApp.client.CanRead(ctx, installation.InstallationID, need.OwnerGitHubID, []string{need.Repository})
			access[need.Repository] = err == nil && readable[need.Repository]
		}
		if err != nil {
			s.logger.Warn("personal machine access check failed", "repo", need.Repository, "err", err)
		}
	}
	if len(access) > 0 {
		if err := tenant.RecordPersonalRepoAccess(ctx, c.Prefix, access); err != nil {
			s.logger.Warn("personal machine access record failed", "err", err)
		}
	}
}
