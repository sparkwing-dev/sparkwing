package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// UnbindAuthorizedGitHubInstallation removes this tenant's installation and its subscriptions.
func (t *Tenant) UnbindAuthorizedGitHubInstallation(ctx context.Context, a capability.Authorized, installation int64) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority})
			set.add(lockTeam, true, string(t.team))
			return discoverInstallationLocks(ctx, tx, set, installation)
		}); err != nil {
			return err
		}
		return t.teamMutationInTx(ctx, tx, a, func(tx *storeTx, _ capability.Principal) error {
			return t.unbindGitHubAppInstallationTx(ctx, tx, installation)
		})
	})
}

// PutAuthorizedGitHubTrigger commits a subscription under current team administration.
func (t *Tenant) PutAuthorizedGitHubTrigger(ctx context.Context, a capability.Authorized, row GitHubAppTrigger) (GitHubAppTrigger, error) {
	var out GitHubAppTrigger
	err := t.teamMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		if row.Team != "" && row.Team != t.team || row.CreatedBy != "" && row.CreatedBy != p.Capability.Subject.ID {
			return bindingRefusal()
		}
		row.CreatedBy = p.Capability.Subject.ID
		var err error
		out, err = t.putGitHubAppTriggerTx(ctx, tx, row, tx.now())
		return err
	})
	if err != nil {
		return GitHubAppTrigger{}, err
	}
	return out, nil
}

// DeleteAuthorizedGitHubTrigger removes only this tenant's exact subscription.
func (t *Tenant) DeleteAuthorizedGitHubTrigger(ctx context.Context, a capability.Authorized, repository int64, pipeline string) error {
	return t.teamMutationTx(ctx, a, func(tx *storeTx, _ capability.Principal) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM github_app_triggers WHERE team=? AND repository_id=? AND pipeline=?`, t.team, repository, pipeline)
		return credentialRowChanged(result, err)
	})
}

// PutAuthorizedGitHubAutomation commits consent for this tenant's active installation.
func (t *Tenant) PutAuthorizedGitHubAutomation(ctx context.Context, a capability.Authorized, row GitHubAppAutomation) error {
	return t.teamMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		if row.EnabledBy != "" && row.EnabledBy != p.Capability.Subject.ID {
			return bindingRefusal()
		}
		if err := t.liveGitHubInstallationTx(ctx, tx, row.InstallationID); err != nil {
			return err
		}
		row.EnabledBy = p.Capability.Subject.ID
		return t.putGitHubAppAutomationTx(ctx, tx, row)
	})
}

// DeleteAuthorizedGitHubAutomation revokes this tenant's repository consent.
func (t *Tenant) DeleteAuthorizedGitHubAutomation(ctx context.Context, a capability.Authorized, repository int64) error {
	return t.teamMutationTx(ctx, a, func(tx *storeTx, _ capability.Principal) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM github_app_automation WHERE team=? AND repository_id=?`, t.team, repository)
		return err
	})
}

// SetAuthorizedGitHubExtraRepos replaces repository slugs and verified identities atomically.
func (t *Tenant) SetAuthorizedGitHubExtraRepos(ctx context.Context, a capability.Authorized, repository string, extras []string, ids map[string]int64) ([]string, error) {
	var out []string
	err := t.teamMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		var err error
		out, err = t.setGitHubExtraReposTx(ctx, tx, repository, extras, ids, p.Capability.Subject.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (t *Tenant) liveGitHubInstallationTx(ctx context.Context, tx *storeTx, installation int64) error {
	var suspended bool
	if err := tx.QueryRowContext(ctx, `SELECT suspended FROM github_app_installations WHERE team=? AND installation_id=?`, t.team, installation).Scan(&suspended); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if suspended {
		return bindingRefusal()
	}
	return nil
}
