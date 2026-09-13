package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const githubWebhookBindingsTableSQLite = `CREATE TABLE IF NOT EXISTS github_webhook_bindings (
    pipeline   TEXT NOT NULL,
    -- lowercase owner/name, so a lookup never depends on the caller's fold
    repo       TEXT NOT NULL,
    -- sealed by the controller's secrets cipher when one is configured
    secret     TEXT NOT NULL,
    -- comma-separated GitHub event names the hook subscribes to
    events     TEXT NOT NULL DEFAULT '',
    hook_id    INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (pipeline, repo)
);
CREATE INDEX IF NOT EXISTS idx_github_webhook_bindings_repo
    ON github_webhook_bindings(repo);`

var githubWebhookBindingsTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").
	Replace(githubWebhookBindingsTableSQLite)

// GitHubWebhookBinding is one repository connected to one pipeline: the
// signing secret its deliveries carry, the events its GitHub webhook
// subscribes to, and the id of that webhook. The controller reads it to
// verify a delivery; `sparkwing cluster webhooks connect` writes it.
type GitHubWebhookBinding struct {
	Pipeline  string
	Repo      string
	Secret    string
	Events    []string
	HookID    int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ErrGitHubWebhookBinding reports a binding a caller named incompletely.
var ErrGitHubWebhookBinding = errors.New("github webhook binding")

// NormalizeGitHubWebhookRepo returns the slug a binding is stored and
// looked up under. Callers that hold a repository slug from another
// source compare through it rather than folding case themselves.
func NormalizeGitHubWebhookRepo(repo string) string {
	return strings.ToLower(strings.TrimSpace(repo))
}

// PutGitHubWebhookBinding stores b, replacing any binding of the same
// pipeline and repository. The creation time of a replaced row is kept,
// so reconnecting a repository does not look like a fresh connection.
func (s *Store) PutGitHubWebhookBinding(ctx context.Context, b GitHubWebhookBinding) error {
	pipeline := strings.TrimSpace(b.Pipeline)
	repo := NormalizeGitHubWebhookRepo(b.Repo)
	if pipeline == "" || repo == "" {
		return fmt.Errorf("%w: pipeline and repo are both required", ErrGitHubWebhookBinding)
	}
	if b.Secret == "" {
		return fmt.Errorf("%w: secret is required", ErrGitHubWebhookBinding)
	}
	now := time.Now().UTC().UnixNano()
	_, err := s.exec(ctx, `
        INSERT INTO github_webhook_bindings
            (pipeline, repo, secret, events, hook_id, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (pipeline, repo) DO UPDATE SET
            secret = excluded.secret,
            events = excluded.events,
            hook_id = excluded.hook_id,
            updated_at = excluded.updated_at`,
		pipeline, repo, b.Secret, strings.Join(b.Events, ","), b.HookID, now, now)
	if err != nil {
		return fmt.Errorf("store github webhook binding: %w", err)
	}
	return nil
}

// ListGitHubWebhookBindings returns the bindings of one pipeline, or
// every binding when pipeline is empty, ordered by repository.
func (s *Store) ListGitHubWebhookBindings(ctx context.Context, pipeline string) (_ []GitHubWebhookBinding, err error) {
	query := `SELECT pipeline, repo, secret, events, hook_id, created_at, updated_at
              FROM github_webhook_bindings`
	args := []any{}
	if pipeline = strings.TrimSpace(pipeline); pipeline != "" {
		query += ` WHERE pipeline = ?`
		args = append(args, pipeline)
	}
	query += ` ORDER BY pipeline, repo`
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list github webhook bindings: %w", err)
	}
	defer closeRowsInto(rows, &err)
	var out []GitHubWebhookBinding
	for rows.Next() {
		b, scanErr := scanGitHubWebhookBinding(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list github webhook bindings: %w", err)
	}
	return out, nil
}

// GetGitHubWebhookBinding returns one binding, or [ErrNotFound] when the
// repository is not connected to that pipeline.
func (s *Store) GetGitHubWebhookBinding(ctx context.Context, pipeline, repo string) (*GitHubWebhookBinding, error) {
	row := s.queryRow(ctx, `
        SELECT pipeline, repo, secret, events, hook_id, created_at, updated_at
        FROM github_webhook_bindings WHERE pipeline = ? AND repo = ?`,
		strings.TrimSpace(pipeline), NormalizeGitHubWebhookRepo(repo))
	b, err := scanGitHubWebhookBinding(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// DeleteGitHubWebhookBinding removes one binding and reports whether a
// row was there to remove.
func (s *Store) DeleteGitHubWebhookBinding(ctx context.Context, pipeline, repo string) (bool, error) {
	res, err := s.exec(ctx,
		`DELETE FROM github_webhook_bindings WHERE pipeline = ? AND repo = ?`,
		strings.TrimSpace(pipeline), NormalizeGitHubWebhookRepo(repo))
	if err != nil {
		return false, fmt.Errorf("delete github webhook binding: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete github webhook binding: %w", err)
	}
	return n > 0, nil
}

func scanGitHubWebhookBinding(row rowScanner) (GitHubWebhookBinding, error) {
	var (
		b                     GitHubWebhookBinding
		events                string
		created, updated      int64
		hookID                int64
		pipeline, repo, value string
	)
	if err := row.Scan(&pipeline, &repo, &value, &events, &hookID, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return b, err
		}
		return b, fmt.Errorf("scan github webhook binding: %w", err)
	}
	b = GitHubWebhookBinding{
		Pipeline:  pipeline,
		Repo:      repo,
		Secret:    value,
		HookID:    hookID,
		CreatedAt: time.Unix(0, created).UTC(),
		UpdatedAt: time.Unix(0, updated).UTC(),
	}
	if events != "" {
		b.Events = strings.Split(events, ",")
	}
	return b, nil
}
