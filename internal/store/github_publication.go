package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// GitHubAppPublication binds source preparation to one repository generation and consent.
// Obtain it before fetching source, then supply it to ReplaceGitHubAppSchedules.
type GitHubAppPublication struct {
	team           Team
	installationID int64
	repositoryID   int64
	generation     int64
	consent        GitHubAppAutomation
}

type githubPublicationResult struct {
	IDs       []string
	Withdrawn []string
}

// ErrGitHubPublicationStale means another publication or consent change superseded preparation.
var ErrGitHubPublicationStale = errors.New("store: GitHub schedule publication superseded; fetch source again")

func githubPublicationEffect(repository int64) string { return fmt.Sprintf("publish:%d", repository) }

// PrepareGitHubAppPublication captures the publication generation before source fetch.
// A true replay result means the delivery's publication already committed.
func (t *Tenant) PrepareGitHubAppPublication(ctx context.Context, installation, repository int64) (GitHubAppPublication, bool, error) {
	p := GitHubAppPublication{team: t.team, installationID: installation, repositoryID: repository}
	replay := false
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		if err := acquireLockSet(ctx, tx, func() lockSet {
			var set lockSet
			set.add(lockTeam, true, string(t.team))
			return tx.operator.discoverInstallationLocks(ctx, tx, set, installation)
		}); err != nil {
			return err
		}
		digest, err := githubDeliveryFenceTx(ctx, tx)
		if err != nil {
			return err
		}
		if digest != "" {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM github_app_delivery_effects WHERE digest=? AND effect=?`, digest, githubPublicationEffect(repository)).Scan(&count); err != nil {
				return err
			}
			if count > 0 {
				replay = true
				return nil
			}
		}
		var enabled int64
		err = tx.QueryRowContext(ctx, `SELECT enabled_at FROM github_app_automation WHERE team=? AND installation_id=? AND repository_id=?`, t.team, installation, repository).Scan(&enabled)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrGitHubAppAutomationRevoked
		}
		if err != nil {
			return err
		}
		p = GitHubAppPublication{team: t.team, installationID: installation, repositoryID: repository, consent: GitHubAppAutomation{InstallationID: installation, RepositoryID: repository, EnabledAt: time.Unix(0, enabled)}}
		if delivery, ok := ctx.Value(githubDeliveryContextKey{}).(GitHubDelivery); ok && p.consent.EnabledAt.After(delivery.ReceivedAt) {
			return ErrGitHubAppAutomationRevoked
		}
		if err := t.checkGitHubAppAutomationTx(ctx, tx, p.consent); err != nil {
			return err
		}
		if err := ensureGitHubPublicationTx(ctx, tx, t.team, repository); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT generation FROM github_app_publications WHERE team=? AND repository_id=?`, t.team, repository).Scan(&p.generation)
	})
	if err != nil {
		return GitHubAppPublication{}, false, err
	}
	return p, replay, nil
}

func ensureGitHubPublicationTx(ctx context.Context, tx *storeTx, team Team, repository int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO github_app_publications(team,repository_id,generation) VALUES (?,?,0) ON CONFLICT DO NOTHING`, team, repository)
	return err
}

func bumpGitHubPublicationTx(ctx context.Context, tx *storeTx, team Team, repository int64) error {
	if err := ensureGitHubPublicationTx(ctx, tx, team, repository); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE github_app_publications SET generation=generation+1 WHERE team=? AND repository_id=?`, team, repository)
	return err
}

func (t *Tenant) checkPublicationTx(ctx context.Context, tx *storeTx, p GitHubAppPublication) error {
	if p.team != t.team || p.repositoryID <= 0 || p.installationID <= 0 {
		return ErrInvalidInput
	}
	if err := t.checkGitHubAppAutomationTx(ctx, tx, p.consent); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE github_app_publications SET generation=generation+1 WHERE team=? AND repository_id=? AND generation=?`, t.team, p.repositoryID, p.generation)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrGitHubPublicationStale
	}
	return err
}

// ReplaceGitHubAppSchedules publishes prepared declarations and their delivery result atomically.
func (t *Tenant) ReplaceGitHubAppSchedules(ctx context.Context, p GitHubAppPublication, repo string, rows []CronSchedule, requests []runspec.Request) ([]string, []string, error) {
	return t.replaceGitHubAppSchedules(ctx, p, repo, rows, requests)
}

func invalidateGitHubInstallationPublicationsTx(ctx context.Context, tx *storeTx, team Team, installation int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE github_app_publications SET generation=generation+1 WHERE team=? AND repository_id IN (SELECT repository_id FROM github_app_automation WHERE team=? AND installation_id=? UNION SELECT github_repository_id FROM cron_schedules WHERE team=? AND github_installation_id=?)`, team, team, installation, team, installation)
	return err
}
