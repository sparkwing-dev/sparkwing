package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// GitHubAppRunner is the owner a repository subscription or automation grant selects for its runs.
type GitHubAppRunner string

// GitHubAppRunnerCloud and GitHubAppRunnerActions are the fixed selections; any other is "pool=<name>".
const (
	GitHubAppRunnerCloud   GitHubAppRunner = "cloud"
	GitHubAppRunnerActions GitHubAppRunner = "github-actions"
)

// ParseGitHubAppRunner accepts cloud (the default), github-actions or pool=<name>.
func ParseGitHubAppRunner(raw string) (GitHubAppRunner, error) {
	switch raw {
	case "", string(GitHubAppRunnerCloud):
		return GitHubAppRunnerCloud, nil
	case string(GitHubAppRunnerActions):
		return GitHubAppRunnerActions, nil
	}
	if pool, ok := strings.CutPrefix(raw, "pool="); ok && ValidRunnerName(pool) {
		return GitHubAppRunner(raw), nil
	}
	return "", fmt.Errorf("%w: runner must be cloud, github-actions or pool=<name>", ErrInvalidInput)
}

func (r GitHubAppRunner) apply(options *runspec.Options) {
	origin := runspec.SettingOrigin{Layer: runspec.LayerOperatorTeam, Key: "github_app.runner"}
	switch {
	case r == GitHubAppRunnerActions:
		options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerActions}}, Origin: origin}
	case strings.HasPrefix(string(r), "pool="):
		if !slices.Contains(options.Requires.Value, string(r)) {
			options.Requires.Value = append(slices.Clone(options.Requires.Value), string(r))
		}
		if !options.Requires.Set {
			options.Requires.Set, options.Requires.Origin = true, origin
		}
	}
}

func trustedAppRunnerTx(ctx context.Context, tx *storeTx, request IntakeRequest) (GitHubAppRunner, error) {
	var raw string
	var err error
	switch {
	case tx.appAdmission != nil && tx.appAdmission.authority() == request.Authority:
		in := tx.appAdmission
		app := in.Request.Git.App.Value
		if in.Automation != nil {
			err = tx.QueryRowContext(ctx, `SELECT runner FROM github_app_automation WHERE team=? AND installation_id=? AND repository_id=?`, in.Installation.Team, app.InstallationID, app.RepositoryID).Scan(&raw)
		} else {
			err = tx.QueryRowContext(ctx, `SELECT runner FROM github_app_triggers WHERE team=? AND installation_id=? AND repository_id=? AND pipeline=?`, in.Installation.Team, app.InstallationID, app.RepositoryID, in.Request.Pipeline).Scan(&raw)
		}
	case tx.scheduleAdmission != nil && tx.scheduleAdmission.authority() == request.Authority && tx.scheduleAdmission.schedule.GitHubInstallationID > 0:
		s := tx.scheduleAdmission.schedule
		err = tx.QueryRowContext(ctx, `SELECT runner FROM github_app_automation WHERE team=? AND installation_id=? AND repository_id=?`, s.Team, s.GitHubInstallationID, s.GitHubRepositoryID).Scan(&raw)
	default:
		return "", nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrGitHubAppAutomationRevoked
	}
	if err != nil {
		return "", err
	}
	return ParseGitHubAppRunner(raw)
}
