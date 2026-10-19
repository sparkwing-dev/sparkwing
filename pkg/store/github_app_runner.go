package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// GitHubAppRunner is the owner a repository subscription or automation grant selects for its runs.
type GitHubAppRunner string

// GitHubAppRunnerAnywhere leaves App runs to the team's claim ranking; cloud, github-actions and "pool=<name>" pin them.
const (
	GitHubAppRunnerAnywhere GitHubAppRunner = "anywhere"
	GitHubAppRunnerCloud    GitHubAppRunner = "cloud"
	GitHubAppRunnerActions  GitHubAppRunner = "github-actions"
)

// ParseGitHubAppRunner accepts anywhere (the default), cloud, github-actions or pool=<name>.
func ParseGitHubAppRunner(raw string) (GitHubAppRunner, error) {
	switch raw {
	case "", string(GitHubAppRunnerAnywhere):
		return GitHubAppRunnerAnywhere, nil
	case string(GitHubAppRunnerCloud), string(GitHubAppRunnerActions):
		return GitHubAppRunner(raw), nil
	}
	if pool, ok := strings.CutPrefix(raw, "pool="); ok && ValidRunnerName(pool) {
		return GitHubAppRunner(raw), nil
	}
	return "", fmt.Errorf("%w: runner must be anywhere, cloud, github-actions or pool=<name>", ErrInvalidInput)
}

func (r GitHubAppRunner) apply(options *runspec.Options) {
	origin := runspec.SettingOrigin{Layer: runspec.LayerOperatorTeam, Key: "github_app.runner"}
	require := func(term string) {
		if !slices.Contains(options.Requires.Value, term) {
			options.Requires.Value = append(slices.Clone(options.Requires.Value), term)
		}
		if !options.Requires.Set {
			options.Requires.Set, options.Requires.Origin = true, origin
		}
	}
	switch {
	case r == GitHubAppRunnerActions:
		options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerActions}}, Origin: origin}
	case r == GitHubAppRunnerCloud:
		require("class=" + match.ClassCloud)
	case strings.HasPrefix(string(r), "pool="):
		require(string(r))
	}
}

// safety: authorized holds only for an App-armed schedule whose automation consent this admission
// validated, so a typed schedule source alone never qualifies for pool access rules.
func trustedAppRunnerTx(ctx context.Context, tx *storeTx, request IntakeRequest) (_ GitHubAppRunner, authorized bool, _ error) {
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
		authorized = string(request.Request.Git.Repository.Key) == fmt.Sprintf("gh:%d", s.GitHubRepositoryID)
	default:
		return "", false, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrGitHubAppAutomationRevoked
	}
	if err != nil {
		return "", false, err
	}
	runner, err := ParseGitHubAppRunner(raw)
	return runner, authorized && err == nil, err
}

func checkActionsOwnerTx(ctx context.Context, tx *storeTx, team Team, input runspec.Request) error {
	if input.Options.Runner.Value.Kind != runspec.RunnerActions {
		return nil
	}
	refused := authorityError(apierr.BadRequest, "the github-actions owner is selected only by GitHub App configuration or inherited from its parent run")
	if input.Lineage.ParentRunID == "" {
		return refused
	}
	parent, err := readRunTx(ctx, tx, team, input.Lineage.ParentRunID)
	if err != nil {
		return err
	}
	if parent.Request.Request.Options.Runner.Value.Kind != runspec.RunnerActions {
		return refused
	}
	return nil
}
