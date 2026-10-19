package controller

import (
	"context"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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
	github, needs, err := tenant.PersonalRepoAccessNeeds(ctx, c.Prefix)
	if err != nil {
		s.logger.Warn("personal machine access check failed", "err", err)
		return
	}
	var stale []runspec.RepoKey
	for repo, fresh := range needs {
		if !fresh {
			stale = append(stale, repo)
		}
	}
	if github > 0 && len(stale) > 0 {
		s.recordPersonalRepoAccess(ctx, tenant, c.Prefix, github, stale)
	}
}

// safety: an automated award to a personal machine rechecks access live, so the cache never outlives a removal.
func (s *Server) confirmPersonalAward(ctx context.Context, claim store.UnitClaim, prefix string) (bool, error) {
	if s.store == nil {
		return true, nil
	}
	tenant, err := s.store.ForTeam(ctx, claim.Team)
	if err != nil {
		return false, err
	}
	github, repos, err := tenant.AwardedAutomation(ctx, claim, prefix)
	if err != nil || github == 0 {
		return err == nil, err
	}
	return s.githubApp != nil && s.recordPersonalRepoAccess(ctx, tenant, prefix, github, repos), nil
}

func (s *Server) recordPersonalRepoAccess(ctx context.Context, tenant *store.Tenant, prefix string, github int64, repos []runspec.RepoKey) bool {
	access := map[runspec.RepoKey]bool{}
	var ids []int64
	for _, repo := range repos {
		access[repo] = false
		raw, keyed := strings.CutPrefix(string(repo), "gh:")
		if id, err := strconv.ParseInt(raw, 10, 64); keyed && err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	installations, err := tenant.GitHubAppInstallations(ctx)
	for _, installation := range installations {
		if err != nil || installation.Suspended || len(ids) != len(repos) {
			break
		}
		readable, readErr := s.githubApp.client.CanRead(ctx, installation.InstallationID, github, ids)
		if readErr != nil {
			continue
		}
		for _, id := range ids {
			access[runspec.RepoKey("gh:"+strconv.FormatInt(id, 10))] = readable[id]
		}
		break
	}
	if err != nil {
		s.logger.Warn("personal machine access check failed", "err", err)
	}
	if err := tenant.RecordPersonalRepoAccess(ctx, prefix, access); err != nil {
		s.logger.Warn("personal machine access record failed", "err", err)
		return false
	}
	for _, allowed := range access {
		if !allowed {
			return false
		}
	}
	return len(access) > 0
}
