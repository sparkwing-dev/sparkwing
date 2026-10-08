package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const githubAppAutomationTable = `CREATE TABLE IF NOT EXISTS github_app_automation (
    team TEXT NOT NULL,
    repository_id BIGINT NOT NULL,
    repository TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    enabled_by TEXT NOT NULL,
    enabled_at BIGINT NOT NULL,
    PRIMARY KEY (team, repository_id),
    FOREIGN KEY (installation_id) REFERENCES github_app_installations(installation_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_github_app_automation_installation ON github_app_automation(installation_id);`

// GitHubAppAutomation grants repository-managed event dispatch through an installation.
type GitHubAppAutomation struct {
	RepositoryID   int64     `json:"repository_id"`
	Repository     string    `json:"repository"`
	InstallationID int64     `json:"installation_id"`
	Enabled        bool      `json:"enabled"`
	EnabledBy      string    `json:"enabled_by"`
	EnabledAt      time.Time `json:"-"`
}

// ErrGitHubAppAutomationRevoked refuses a plan whose consent is no longer current.
var ErrGitHubAppAutomationRevoked = errors.New("store: repository automation consent is no longer current")

// CreateGitHubAutomationTriggerWithRun admits an automatic run only under the consent generation that planned it.
func (t *Tenant) CreateGitHubAutomationTriggerWithRun(ctx context.Context, trig Trigger, run Run, grant GitHubAppAutomation) error {
	if grant.InstallationID <= 0 || grant.RepositoryID <= 0 || grant.EnabledAt.IsZero() || trig.GithubRepoID != grant.RepositoryID {
		return ErrInvalidInput
	}
	return t.createTriggerWithRun(ctx, trig, run, "", "", "", &grant)
}

func (t *Tenant) checkGitHubAppAutomationTx(ctx context.Context, tx *storeTx, grant GitHubAppAutomation) error {
	// safety: writing both authority rows serializes admission with suspension, unbinding and consent deletion.
	res, err := tx.ExecContext(ctx, `UPDATE github_app_installations SET updated_at = updated_at
	    WHERE team = ? AND installation_id = ? AND suspended = 0`, string(t.team), grant.InstallationID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrGitHubAppAutomationRevoked
	}
	res, err = tx.ExecContext(ctx, `UPDATE github_app_automation SET enabled_at = enabled_at
	    WHERE team = ? AND installation_id = ? AND repository_id = ? AND enabled_at = ?`,
		string(t.team), grant.InstallationID, grant.RepositoryID, grant.EnabledAt.UnixNano())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrGitHubAppAutomationRevoked
	}
	return nil
}

// PutGitHubAppAutomation records consent only for an installation the team holds.
func (t *Tenant) PutGitHubAppAutomation(ctx context.Context, a GitHubAppAutomation, now time.Time) error {
	repo, ok := ParseGitHubRepo(a.Repository)
	if !ok || a.RepositoryID <= 0 || a.InstallationID <= 0 || a.EnabledBy == "" {
		return fmt.Errorf("%w: automation needs a repository, installation and granting account", ErrInvalidInput)
	}
	_, err := t.s.exec(ctx, `INSERT INTO github_app_automation
	    (team, repository_id, repository, installation_id, enabled_by, enabled_at)
	    SELECT ?, ?, ?, ?, ?, ? WHERE EXISTS
	    (SELECT 1 FROM github_app_installations WHERE team = ? AND installation_id = ? AND suspended = 0)
	    ON CONFLICT (team, repository_id) DO UPDATE SET
	    repository = excluded.repository, installation_id = excluded.installation_id,
	    enabled_by = excluded.enabled_by,
	    enabled_at = CASE WHEN github_app_automation.installation_id = excluded.installation_id
	        THEN github_app_automation.enabled_at ELSE excluded.enabled_at END`,
		string(t.team), a.RepositoryID, repo.Slug(), a.InstallationID, a.EnabledBy, now.UnixNano(), string(t.team), a.InstallationID)
	if err != nil {
		return err
	}
	_, err = t.GitHubAppAutomation(ctx, a.InstallationID, a.RepositoryID)
	return err
}

// GitHubAppAutomation reads live consent for one repository and installation.
func (t *Tenant) GitHubAppAutomation(ctx context.Context, installation, repository int64) (GitHubAppAutomation, error) {
	var a GitHubAppAutomation
	var enabled int64
	err := t.s.queryRow(ctx, `SELECT a.repository_id, a.repository, a.installation_id, a.enabled_by, a.enabled_at
	    FROM github_app_automation a JOIN github_app_installations i ON i.installation_id = a.installation_id AND i.team = a.team
	    WHERE a.team = ? AND a.installation_id = ? AND a.repository_id = ? AND i.suspended = 0`,
		string(t.team), installation, repository).Scan(&a.RepositoryID, &a.Repository, &a.InstallationID, &a.EnabledBy, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.Enabled, a.EnabledAt = err == nil, time.Unix(0, enabled)
	return a, err
}

// GitHubAppAutomations lists the team's grants, including suspended installations.
func (t *Tenant) GitHubAppAutomations(ctx context.Context) (_ []GitHubAppAutomation, err error) {
	rows, err := t.s.query(ctx, `SELECT repository_id, repository, installation_id, enabled_by, enabled_at FROM github_app_automation WHERE team = ? ORDER BY repository`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []GitHubAppAutomation{}
	for rows.Next() {
		var a GitHubAppAutomation
		var enabled int64
		if err := rows.Scan(&a.RepositoryID, &a.Repository, &a.InstallationID, &a.EnabledBy, &enabled); err != nil {
			return nil, err
		}
		a.Enabled, a.EnabledAt = true, time.Unix(0, enabled)
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteGitHubAppAutomation revokes a team's grant without affecting manual subscriptions.
func (t *Tenant) DeleteGitHubAppAutomation(ctx context.Context, repository int64) error {
	_, err := t.s.exec(ctx, `DELETE FROM github_app_automation WHERE team = ? AND repository_id = ?`, string(t.team), repository)
	return err
}

// WithdrawGitHubAppAutomation removes consent only through its bound installation.
func (t *Tenant) WithdrawGitHubAppAutomation(ctx context.Context, installation, repository int64) error {
	_, err := t.s.exec(ctx, `DELETE FROM github_app_automation WHERE team = ? AND installation_id = ? AND repository_id = ?`, string(t.team), installation, repository)
	return err
}

// RenameGitHubAppAutomation follows a repository's numeric identity after a rename.
func (t *Tenant) RenameGitHubAppAutomation(ctx context.Context, installation, repository int64, name string) error {
	_, err := t.s.exec(ctx, `UPDATE github_app_automation SET repository = ? WHERE team = ? AND installation_id = ? AND repository_id = ?`, name, string(t.team), installation, repository)
	return err
}
