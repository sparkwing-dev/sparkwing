package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// GitHubAppInstallation binds one installation of the deployment's GitHub
// App to the team whose owner connected it. An installation belongs to at
// most one team, because the installation is what proves control of the
// repositories it covers.
type GitHubAppInstallation struct {
	Team           Team
	InstallationID int64
	// AccountID and AccountLogin name the GitHub account the App is
	// installed on; AccountType is "User" or "Organization".
	AccountID    int64
	AccountLogin string
	AccountType  string
	Suspended    bool
	// ConnectedBy is the Sparkwing account that connected it, and
	// GitHubUserID the GitHub user that account proved it was.
	ConnectedBy  string
	GitHubUserID int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// GitHubAppTrigger subscribes one pipeline to one repository's events
// arriving through the App.
type GitHubAppTrigger struct {
	Team                      Team
	RepositoryID              int64
	Repository                string
	InstallationID            int64
	Pipeline                  string
	Push                      bool
	Tags                      []string
	PullRequest               bool
	Branches                  []string
	BaseBranches              []string
	PullRequestClosed         bool
	PullRequestLabeled        bool
	PullRequestLabels         []string
	PullRequestReadyForReview bool
	ReleasePublished          bool
	ReleasePrereleased        bool
	BranchCreate              bool
	BranchDelete              bool
	CreatedBy                 string
	CreatedAt                 time.Time
}

// ErrInstallationBoundElsewhere is returned when a team connects an
// installation another team already holds.
var ErrInstallationBoundElsewhere = errors.New("store: this GitHub App installation is connected to another team")

const (
	maxGitHubTagPatterns      = 10
	maxGitHubTagPatternLength = 128
)

// ValidateGitHubTagPatterns checks the limits and glob syntax of a subscription's tag patterns.
func ValidateGitHubTagPatterns(patterns []string) error {
	if len(patterns) > maxGitHubTagPatterns {
		return fmt.Errorf("%w: at most %d tag patterns are allowed", ErrInvalidInput, maxGitHubTagPatterns)
	}
	for _, pattern := range patterns {
		if pattern == "" || len(pattern) > maxGitHubTagPatternLength {
			return fmt.Errorf("%w: tag patterns must have 1 to %d bytes", ErrInvalidInput, maxGitHubTagPatternLength)
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("%w: invalid tag pattern %q: %w", ErrInvalidInput, pattern, err)
		}
	}
	return nil
}

const githubAppInstallationCols = `installation_id, team, account_id, account_login, account_type, suspended,
       connected_by, github_user_id, created_at, updated_at`

func scanGitHubAppInstallation(row interface{ Scan(...any) error }) (GitHubAppInstallation, error) {
	var in GitHubAppInstallation
	var team string
	var suspended int
	var created, updated int64
	if err := row.Scan(&in.InstallationID, &team, &in.AccountID, &in.AccountLogin, &in.AccountType, &suspended,
		&in.ConnectedBy, &in.GitHubUserID, &created, &updated); err != nil {
		return GitHubAppInstallation{}, err
	}
	in.Team, in.Suspended = Team(team), suspended != 0
	in.CreatedAt, in.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return in, nil
}

// BindGitHubAppInstallation binds in to t, or refreshes the account and
// connector of a binding t already holds. An installation another team holds
// is [ErrInstallationBoundElsewhere], and the binding stays where it is.
func (t *Tenant) BindGitHubAppInstallation(ctx context.Context, in GitHubAppInstallation, now time.Time) (GitHubAppInstallation, error) {
	if in.InstallationID <= 0 || in.AccountID <= 0 || in.AccountLogin == "" || in.AccountType == "" {
		return GitHubAppInstallation{}, fmt.Errorf("%w: a binding needs the installation and its account", ErrInvalidInput)
	}
	at := now.UTC().Unix()
	suspended := 0
	if in.Suspended {
		suspended = 1
	}
	// safety: the conflict guard compares teams, so a second team's connect
	// updates nothing and cannot take the installation over.
	res, err := t.s.exec(ctx, `
		INSERT INTO github_app_installations (`+githubAppInstallationCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (installation_id) DO UPDATE SET
		    account_id = excluded.account_id, account_login = excluded.account_login,
		    account_type = excluded.account_type, suspended = excluded.suspended,
		    connected_by = excluded.connected_by, github_user_id = excluded.github_user_id,
		    updated_at = excluded.updated_at
		WHERE github_app_installations.team = excluded.team`,
		in.InstallationID, string(t.team), in.AccountID, in.AccountLogin, in.AccountType, suspended,
		in.ConnectedBy, in.GitHubUserID, at, at)
	if err != nil {
		return GitHubAppInstallation{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return GitHubAppInstallation{}, err
		}
		return GitHubAppInstallation{}, ErrInstallationBoundElsewhere
	}
	return t.GitHubAppInstallation(ctx, in.InstallationID)
}

// GitHubAppInstallation reads t's binding of installation, or ErrNotFound.
func (t *Tenant) GitHubAppInstallation(ctx context.Context, installation int64) (GitHubAppInstallation, error) {
	in, err := scanGitHubAppInstallation(t.s.queryRow(ctx, `SELECT `+githubAppInstallationCols+`
		FROM github_app_installations WHERE team = ? AND installation_id = ?`, string(t.team), installation))
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubAppInstallation{}, ErrNotFound
	}
	return in, err
}

// GitHubAppInstallations lists t's bindings, oldest first.
func (t *Tenant) GitHubAppInstallations(ctx context.Context) (_ []GitHubAppInstallation, err error) {
	rows, err := t.s.query(ctx, `SELECT `+githubAppInstallationCols+`
		FROM github_app_installations WHERE team = ? ORDER BY created_at, installation_id`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []GitHubAppInstallation
	for rows.Next() {
		in, err := scanGitHubAppInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// GitHubAppInstallationTeam answers the team installation is bound to, or
// ErrNotFound. A webhook delivery and a repository's installation name no
// team, so this read is how either finds one.
func (o *Operator) GitHubAppInstallationTeam(ctx context.Context, installation int64) (GitHubAppInstallation, error) {
	in, err := scanGitHubAppInstallation(o.s.queryRow(ctx, `SELECT `+githubAppInstallationCols+`
		FROM github_app_installations WHERE installation_id = ?`, installation))
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubAppInstallation{}, ErrNotFound
	}
	return in, err
}

// UnbindGitHubAppInstallation removes installation's binding from whichever
// team holds it, with that team's subscriptions to it, and reports the team.
// GitHub's uninstall notice and the operator's move both name only the
// installation.
func (o *Operator) UnbindGitHubAppInstallation(ctx context.Context, installation int64) (Team, error) {
	in, err := o.GitHubAppInstallationTeam(ctx, installation)
	if err != nil {
		return "", err
	}
	t, err := o.s.ForTeam(ctx, in.Team)
	if err != nil {
		return "", err
	}
	return in.Team, o.s.inTx(ctx, func(tx *storeTx) error {
		return t.unbindGitHubAppInstallationTx(ctx, tx, installation)
	})
}

// SetGitHubAppInstallationSuspended records GitHub's suspension of
// installation. An unbound installation is ErrNotFound.
func (o *Operator) SetGitHubAppInstallationSuspended(ctx context.Context, installation int64, suspended bool, now time.Time) error {
	in, err := o.GitHubAppInstallationTeam(ctx, installation)
	if err != nil {
		return err
	}
	flag := 0
	if suspended {
		flag = 1
	}
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if _, err := tx.ExecContext(ctx, `UPDATE github_app_installations SET suspended = ?, updated_at = ?
		WHERE team = ? AND installation_id = ?`, flag, now.UTC().Unix(), string(in.Team), installation); err != nil {
		return err
	}
	if suspended {
		if _, err := tx.ExecContext(ctx, `UPDATE cron_schedules SET declared = 0, updated_at = ?
			WHERE team = ? AND github_installation_id = ? AND declared = 1`,
			now.UnixNano(), string(in.Team), installation); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const githubAppTriggerCols = `repository_id, pipeline, repository, installation_id, on_push, tag_patterns, on_pull_request,
       branches, base_branches, created_by, created_at, on_pr_closed, on_pr_labeled, pr_labels, on_pr_ready_for_review,
       on_release_published, on_release_prereleased, on_branch_create, on_branch_delete`

func (t *Tenant) scanGitHubAppTriggers(rows *sql.Rows) ([]GitHubAppTrigger, error) {
	var out []GitHubAppTrigger
	for rows.Next() {
		tr := GitHubAppTrigger{Team: t.team}
		var push, pr, closed, labeled, ready, published, prereleased, create, deleted int
		var patterns, branches, baseBranches, labels string
		var created int64
		if err := rows.Scan(&tr.RepositoryID, &tr.Pipeline, &tr.Repository, &tr.InstallationID, &push, &patterns, &pr,
			&branches, &baseBranches, &tr.CreatedBy, &created,
			&closed, &labeled, &labels, &ready, &published, &prereleased, &create, &deleted); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(patterns), &tr.Tags); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(branches), &tr.Branches); err != nil {
			return nil, fmt.Errorf("decode push branches: %w", err)
		}
		if err := json.Unmarshal([]byte(baseBranches), &tr.BaseBranches); err != nil {
			return nil, fmt.Errorf("decode pull request base branches: %w", err)
		}
		tr.Push, tr.PullRequest = push != 0, pr != 0
		tr.PullRequestClosed, tr.PullRequestLabeled, tr.PullRequestReadyForReview = closed != 0, labeled != 0, ready != 0
		tr.ReleasePublished, tr.ReleasePrereleased = published != 0, prereleased != 0
		tr.BranchCreate, tr.BranchDelete = create != 0, deleted != 0
		if err := json.Unmarshal([]byte(labels), &tr.PullRequestLabels); err != nil {
			return nil, err
		}
		tr.CreatedAt = time.Unix(0, created).UTC()
		out = append(out, tr)
	}
	return out, rows.Err()
}

func (t *Tenant) putGitHubAppTriggerTx(ctx context.Context, tx *storeTx, tr GitHubAppTrigger, now time.Time) (GitHubAppTrigger, error) {
	repo, ok := ParseGitHubRepo(tr.Repository)
	if !ok || tr.RepositoryID <= 0 || strings.TrimSpace(tr.Pipeline) == "" ||
		(!tr.Push && len(tr.Tags) == 0 && !tr.PullRequest && !tr.PullRequestClosed && !tr.PullRequestLabeled && !tr.PullRequestReadyForReview &&
			!tr.ReleasePublished && !tr.ReleasePrereleased && !tr.BranchCreate && !tr.BranchDelete) {
		return GitHubAppTrigger{}, fmt.Errorf("%w: a subscription needs a repository, a pipeline and at least one event", ErrInvalidInput)
	}
	if err := ValidateGitHubTagPatterns(tr.Tags); err != nil {
		return GitHubAppTrigger{}, err
	}
	if err := validateGitHubBranchPatterns("branches", tr.Branches); err != nil {
		return GitHubAppTrigger{}, err
	}
	if err := validateGitHubBranchPatterns("base_branches", tr.BaseBranches); err != nil {
		return GitHubAppTrigger{}, err
	}
	if tr.Branches == nil {
		tr.Branches = []string{}
	}
	if tr.BaseBranches == nil {
		tr.BaseBranches = []string{}
	}
	if tr.PullRequestLabeled && len(tr.PullRequestLabels) == 0 {
		return GitHubAppTrigger{}, fmt.Errorf("%w: labeled pull requests need a label filter", ErrInvalidInput)
	}
	for _, label := range tr.PullRequestLabels {
		if strings.TrimSpace(label) == "" {
			return GitHubAppTrigger{}, fmt.Errorf("%w: empty pull request label", ErrInvalidInput)
		}
	}
	labels, err := json.Marshal(tr.PullRequestLabels)
	if err != nil {
		return GitHubAppTrigger{}, err
	}
	if err := t.liveGitHubInstallationTx(ctx, tx, tr.InstallationID); err != nil {
		return GitHubAppTrigger{}, err
	}
	tr.Team, tr.Repository, tr.CreatedAt = t.team, repo.Slug(), now.UTC()
	if tr.Tags == nil {
		tr.Tags = []string{}
	}
	patterns, err := json.Marshal(tr.Tags)
	if err != nil {
		return GitHubAppTrigger{}, err
	}
	flag := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	branches, err := json.Marshal(tr.Branches)
	if err != nil {
		return GitHubAppTrigger{}, fmt.Errorf("encode push branches: %w", err)
	}
	baseBranches, err := json.Marshal(tr.BaseBranches)
	if err != nil {
		return GitHubAppTrigger{}, fmt.Errorf("encode pull request base branches: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO github_app_triggers (team, repo_key, `+githubAppTriggerCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (team, repository_id, pipeline) DO UPDATE SET
		    repository = excluded.repository, installation_id = excluded.installation_id,
		    on_push = excluded.on_push, tag_patterns = excluded.tag_patterns, on_pull_request = excluded.on_pull_request,
		    branches = excluded.branches, base_branches = excluded.base_branches,
		    on_pr_closed = excluded.on_pr_closed, on_pr_labeled = excluded.on_pr_labeled,
		    pr_labels = excluded.pr_labels, on_pr_ready_for_review = excluded.on_pr_ready_for_review,
		    on_release_published = excluded.on_release_published, on_release_prereleased = excluded.on_release_prereleased,
		    on_branch_create = excluded.on_branch_create, on_branch_delete = excluded.on_branch_delete,
		    created_by = excluded.created_by, created_at = excluded.created_at`,
		string(t.team), fmt.Sprintf("gh:%d", tr.RepositoryID), tr.RepositoryID, tr.Pipeline, tr.Repository, tr.InstallationID, flag(tr.Push), string(patterns),
		flag(tr.PullRequest), string(branches), string(baseBranches), tr.CreatedBy, tr.CreatedAt.UnixNano(),
		flag(tr.PullRequestClosed), flag(tr.PullRequestLabeled), string(labels), flag(tr.PullRequestReadyForReview),
		flag(tr.ReleasePublished), flag(tr.ReleasePrereleased), flag(tr.BranchCreate), flag(tr.BranchDelete))
	if err != nil {
		return GitHubAppTrigger{}, err
	}
	return tr, nil
}

func validateGitHubBranchPatterns(field string, patterns []string) error {
	if len(patterns) > 10 {
		return fmt.Errorf("%w: %s has more than 10 patterns", ErrInvalidInput, field)
	}
	for _, pattern := range patterns {
		if pattern == "" || len(pattern) > 128 {
			return fmt.Errorf("%w: each %s pattern must be 1 to 128 bytes", ErrInvalidInput, field)
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("%w: invalid %s pattern %q: %w", ErrInvalidInput, field, pattern, err)
		}
	}
	return nil
}

// GitHubAppTriggers lists t's subscriptions by repository then pipeline.
func (t *Tenant) GitHubAppTriggers(ctx context.Context) (_ []GitHubAppTrigger, err error) {
	rows, err := t.s.query(ctx, `SELECT `+githubAppTriggerCols+` FROM github_app_triggers
		WHERE team = ? ORDER BY repository, pipeline`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	return t.scanGitHubAppTriggers(rows)
}

// GitHubAppTriggersFor lists t's subscriptions to repositoryID through
// installation.
func (t *Tenant) GitHubAppTriggersFor(ctx context.Context, installation, repositoryID int64) (_ []GitHubAppTrigger, err error) {
	rows, err := t.s.query(ctx, `SELECT `+githubAppTriggerCols+` FROM github_app_triggers
		WHERE team = ? AND installation_id = ? AND repository_id = ? ORDER BY pipeline`,
		string(t.team), installation, repositoryID)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	return t.scanGitHubAppTriggers(rows)
}

// RenameGitHubAppTriggerRepository updates subscriptions under the repository's
// stable GitHub id when its name or installation changes.
func (t *Tenant) RenameGitHubAppTriggerRepository(ctx context.Context, installation, repositoryID int64, name string) error {
	repo, ok := ParseGitHubRepo(name)
	if !ok {
		return ErrInvalidInput
	}
	_, err := t.s.exec(ctx, `UPDATE github_app_triggers SET repository = ?, installation_id = ? WHERE team = ? AND repository_id = ?`,
		repo.Slug(), installation, string(t.team), repositoryID)
	return err
}

// AccountIdentity returns the subject of accountID's identity at provider,
// such as the numeric GitHub user id, or ErrNotFound when the account has
// none linked.
func (s *Store) AccountIdentity(ctx context.Context, accountID, provider string) (string, error) {
	var subject string
	err := s.queryRow(ctx, `SELECT subject FROM identities WHERE account_id = ? AND provider = ?
		ORDER BY created_at LIMIT 1`, accountID, provider).Scan(&subject)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return subject, err
}

// ConsumeGitHubAppConnectState records that the connect flow named by nonce
// finished, and reports false when one already did. The record lives until
// the state expires, so every replica and a restarted controller refuse a
// state that was used once.
func (s *Store) ConsumeGitHubAppConnectState(ctx context.Context, nonce string, expires, now time.Time) (bool, error) {
	return s.consumeFlowNonce(ctx, "github_app_connect", nonce, expires)
}

// GitHubAppDeliveryRetention is how long a delivery's digest is remembered.
// The App webhook refuses an event older than this, so a forgotten digest
// does not readmit a replayed delivery.
const GitHubAppDeliveryRetention = 90 * 24 * time.Hour

// BindGitHubAppDeliveryEvent ties digest to the first event it arrived as and
// reports whether event is that one. The binding is kept apart from the
// processed record, so binding a delivery does not make it seen.
func (s *Store) BindGitHubAppDeliveryEvent(ctx context.Context, digest, event, delivery string, now time.Time) (bool, error) {
	if err := s.forgetExpiredGitHubAppDeliveries(ctx, now); err != nil {
		return false, err
	}
	key := "event:" + digest
	if _, err := s.exec(ctx, `INSERT INTO github_app_deliveries (digest, delivery_id, received_at, event) VALUES (?, ?, ?, ?)
		ON CONFLICT (digest) DO NOTHING`, key, delivery, now.Unix(), event); err != nil {
		return false, err
	}
	var bound string
	if err := s.queryRow(ctx, `SELECT event FROM github_app_deliveries WHERE digest = ?`, key).Scan(&bound); err != nil {
		return false, err
	}
	return bound == event, nil
}

// GitHubAppDeliverySeen reports whether a delivery with digest was processed
// before, for any team.
func (s *Store) GitHubAppDeliverySeen(ctx context.Context, digest string) (bool, error) {
	var n int
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM github_app_deliveries WHERE digest = ?`, digest).Scan(&n)
	return n > 0, err
}

// RecordGitHubAppDelivery remembers that a delivery with digest was
// processed. It and [Store.BindGitHubAppDeliveryEvent] forget digests and
// event bindings older than [GitHubAppDeliveryRetention].
func (s *Store) RecordGitHubAppDelivery(ctx context.Context, digest, delivery string, now time.Time) error {
	if err := s.forgetExpiredGitHubAppDeliveries(ctx, now); err != nil {
		return err
	}
	_, err := s.exec(ctx, `INSERT INTO github_app_deliveries (digest, delivery_id, received_at) VALUES (?, ?, ?)
		ON CONFLICT (digest) DO NOTHING`, digest, delivery, now.Unix())
	return err
}

func (s *Store) forgetExpiredGitHubAppDeliveries(ctx context.Context, now time.Time) error {
	_, err := s.exec(ctx, `DELETE FROM github_app_deliveries WHERE received_at <= ?`,
		now.Add(-GitHubAppDeliveryRetention).Unix())
	return err
}

// GitHubCheckRun returns the GitHub check run recorded for t's run runID, or
// 0 when none is. ErrNotFound when t has no trigger runID.
func (t *Tenant) GitHubCheckRun(ctx context.Context, runID string) (int64, error) {
	var id int64
	err := t.s.queryRow(ctx, `SELECT github_check_run_id FROM triggers WHERE team = ? AND id = ?`,
		string(t.team), runID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, notFound("trigger", runID)
	}
	return id, err
}

// RecordGitHubCheckRun records checkRun as the GitHub check run reporting
// t's run runID unless one is already recorded, and returns the one recorded.
func (t *Tenant) RecordGitHubCheckRun(ctx context.Context, runID string, checkRun int64) (int64, error) {
	if checkRun <= 0 {
		return 0, fmt.Errorf("%w: a check run id is positive", ErrInvalidInput)
	}
	if _, err := t.s.exec(ctx, `UPDATE triggers SET github_check_run_id = ?
		WHERE team = ? AND id = ? AND github_check_run_id = 0`, checkRun, string(t.team), runID); err != nil {
		return 0, err
	}
	return t.GitHubCheckRun(ctx, runID)
}

// ReportGitHubCheck runs fn while t holds runID's GitHub report lock, so one
// controller at a time creates or moves the check run or commit status that
// reports the run. fn gets the recorded check run id, 0 when none is, and
// returns the id of a check run it created, or 0; that id is recorded before
// the lock is released. ErrNotFound when t has no trigger runID.
//
// safety: on Postgres the lock is a transaction-scoped advisory lock and the
// id is recorded in that transaction, so a holder cut off by its deadline
// releases the lock and its record together; a SQLite store serves one
// controller process, so a lock in the Store covers every reporter in it.
func (t *Tenant) ReportGitHubCheck(ctx context.Context, runID string, fn func(ctx context.Context, checkRun int64) (int64, error)) error {
	if t.s.dialect != DialectPostgres {
		t.s.githubCheckMu.Lock()
		defer t.s.githubCheckMu.Unlock()
		id, err := t.GitHubCheckRun(ctx, runID)
		if err != nil {
			return err
		}
		if id, err = fn(ctx, id); err != nil || id == 0 {
			return err
		}
		_, err = t.RecordGitHubCheckRun(ctx, runID, id)
		return err
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`,
		"sparkwing/github-check/"+string(t.team)+"/"+runID); err != nil {
		return err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT github_check_run_id FROM triggers WHERE team = ? AND id = ?`,
		string(t.team), runID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("trigger", runID)
	}
	if err != nil {
		return err
	}
	if id, err = fn(ctx, id); err != nil {
		return err
	}
	if id != 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE triggers SET github_check_run_id = ?
			WHERE team = ? AND id = ? AND github_check_run_id = 0`, id, string(t.team), runID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const maxGitHubCommitTriggers = 100

// GitHubCommitTriggers reads sealed trigger reports by numeric repository identity and exact commit.
func (t *Tenant) GitHubCommitTriggers(ctx context.Context, repo GitHubRepo, sha string) (_ []*Trigger, err error) {
	if repo.ID <= 0 {
		return nil, nil
	}
	rows, err := t.s.query(ctx, triggerSelect+` WHERE runs.team=? AND runs.repo_key=? AND runs.git_sha=? ORDER BY triggers.created_at DESC,triggers.id DESC LIMIT ?`, t.team, githubRepositoryKey(repo.ID), sha, maxGitHubCommitTriggers)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []*Trigger
	for rows.Next() {
		tr, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}

func (t *Tenant) unbindGitHubAppInstallationTx(ctx context.Context, tx *storeTx, installation int64) error {
	res, err := tx.ExecContext(ctx, `DELETE FROM github_app_installations WHERE team = ? AND installation_id = ?`,
		string(t.team), installation)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM github_app_triggers WHERE team = ? AND installation_id = ?`,
		string(t.team), installation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cron_schedules SET declared = 0, updated_at = ?
		WHERE team = ? AND github_installation_id = ? AND declared = 1`,
		tx.now().UnixNano(), string(t.team), installation); err != nil {
		return err
	}
	return nil
}
