package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// GitHubRunnerPrincipalPrefix begins the principal of every credential a
// GitHub Actions job exchanges its ID token for. The controller reads the
// bound repository back out of the principal, so no other mint path may
// produce a principal with this prefix.
const GitHubRunnerPrincipalPrefix = "github:"

// GitHubRunnerBinding is a team owner's consent that workflow jobs of one
// GitHub repository may run that team's work for that repository.
type GitHubRunnerBinding struct {
	Team Team
	// RepositoryID is GitHub's numeric id, the key a job's ID token is
	// matched on, because a repository name can be renamed or re-registered
	// by someone else.
	RepositoryID int64
	// RepositoryOwnerID pins the account that owned the repository when the
	// binding was made, so a transferred repository stops matching.
	RepositoryOwnerID int64
	// Repository is "owner/name" as the owner entered it, for display.
	Repository string
	CreatedBy  string
	CreatedAt  time.Time
}

const githubRunnerBindingsTableSQLite = `
CREATE TABLE IF NOT EXISTS github_runner_bindings (
    team                TEXT NOT NULL,
    repository_id       INTEGER NOT NULL,
    repository_owner_id INTEGER NOT NULL,
    repository          TEXT NOT NULL,
    created_by          TEXT NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    PRIMARY KEY (team, repository_id)
)`

var githubRunnerBindingsTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(githubRunnerBindingsTableSQLite)

// github_runner_credentials records the push each GitHub Actions credential
// was minted for, keyed by the token's prefix.
const githubRunnerCredentialsTableSQLite = `
CREATE TABLE IF NOT EXISTS github_runner_credentials (
    team       TEXT NOT NULL,
    prefix     TEXT NOT NULL,
    branch     TEXT NOT NULL,
    sha        TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (team, prefix)
)`

var githubRunnerCredentialsTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(githubRunnerCredentialsTableSQLite)

// applyGitHubRunnerBindingsMigration creates the bindings and credentials
// tables. It is a step of the identity migration rather than a schema version
// of its own.
func applyGitHubRunnerBindingsMigration(ctx context.Context, tx *storeTx, ddl ...string) error {
	for _, stmt := range ddl {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// GitHubRunnerBindings lists t's bindings, oldest first.
func (t *Tenant) GitHubRunnerBindings(ctx context.Context) (_ []GitHubRunnerBinding, err error) {
	rows, err := t.s.query(ctx, `
		SELECT repository_id, repository_owner_id, repository, created_by, created_at
		FROM github_runner_bindings WHERE team = ? ORDER BY created_at, repository_id`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	var out []GitHubRunnerBinding
	for rows.Next() {
		b := GitHubRunnerBinding{Team: t.team}
		var created int64
		if err := rows.Scan(&b.RepositoryID, &b.RepositoryOwnerID, &b.Repository, &b.CreatedBy, &created); err != nil {
			return nil, err
		}
		b.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

// GitHubRunnerBinding reads t's binding for a repository id, or ErrNotFound.
func (t *Tenant) GitHubRunnerBinding(ctx context.Context, repositoryID int64) (GitHubRunnerBinding, error) {
	b := GitHubRunnerBinding{Team: t.team, RepositoryID: repositoryID}
	var created int64
	err := t.s.queryRow(ctx, `
		SELECT repository_owner_id, repository, created_by, created_at
		FROM github_runner_bindings WHERE team = ? AND repository_id = ?`,
		string(t.team), repositoryID).Scan(&b.RepositoryOwnerID, &b.Repository, &b.CreatedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubRunnerBinding{}, ErrNotFound
	}
	if err != nil {
		return GitHubRunnerBinding{}, err
	}
	b.CreatedAt = time.Unix(created, 0).UTC()
	return b, nil
}

// AddGitHubRunnerBinding records b for t. Binding a repository id t already
// binds is ErrAlreadyBound, and a binding past [MaxGitHubRunnerBindings] is
// ErrBindingLimit; the operator's team is not capped.
func (t *Tenant) AddGitHubRunnerBinding(ctx context.Context, b GitHubRunnerBinding, now time.Time) (GitHubRunnerBinding, error) {
	repo, ok := ParseGitHubRepo(b.Repository)
	if !ok || b.RepositoryID <= 0 || b.RepositoryOwnerID <= 0 {
		return GitHubRunnerBinding{}, fmt.Errorf("%w: a binding needs owner/name, a repository id and an owner id", ErrInvalidInput)
	}
	b.Team, b.Repository, b.CreatedAt = t.team, repo.Slug(), now.UTC().Truncate(time.Second)
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return GitHubRunnerBinding{}, err
	}
	defer rollbackOrLog(tx)
	if holdsFreeAllowance(t.team) {
		if err := t.lockTeamTx(ctx, tx); err != nil {
			return GitHubRunnerBinding{}, err
		}
		var bound int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM github_runner_bindings WHERE team = ?`,
			string(t.team)).Scan(&bound); err != nil {
			return GitHubRunnerBinding{}, err
		}
		if bound >= MaxGitHubRunnerBindings {
			return GitHubRunnerBinding{}, fmt.Errorf("%w: team %s binds %d repositories, the most a team may",
				ErrBindingLimit, t.team, bound)
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO github_runner_bindings (team, repository_id, repository_owner_id, repository, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		string(t.team), b.RepositoryID, b.RepositoryOwnerID, b.Repository, b.CreatedBy, b.CreatedAt.Unix())
	if isUniqueViolation(err) {
		return GitHubRunnerBinding{}, ErrAlreadyBound
	}
	if err != nil {
		return GitHubRunnerBinding{}, err
	}
	return b, tx.Commit()
}

// MaxGitHubRunnerBindings bounds the repositories one team binds to GitHub
// runners, funded or not.
const MaxGitHubRunnerBindings = 20

// ErrBindingLimit refuses a GitHub runner binding past
// [MaxGitHubRunnerBindings].
var ErrBindingLimit = errors.New("store: this team binds as many repositories as it may")

// ErrAlreadyBound is returned when a team binds a repository it already binds.
var ErrAlreadyBound = errors.New("store: this repository is already bound to the team")

// RemoveGitHubRunnerBinding deletes t's binding for a repository id and
// revokes the live credentials minted under it. An unknown id is ErrNotFound.
func (t *Tenant) RemoveGitHubRunnerBinding(ctx context.Context, repositoryID int64, now time.Time) (revoked []string, err error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	res, err := tx.ExecContext(ctx, `DELETE FROM github_runner_bindings WHERE team = ? AND repository_id = ?`,
		string(t.team), repositoryID)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return nil, ErrNotFound
	}
	at := now.UTC().Unix()
	like := GitHubRunnerPrincipalPrefix + fmt.Sprint(repositoryID) + ":%"
	revoked, err = livePrefixesLike(ctx, tx, t.team, like, at)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE tokens SET revoked_at = ?
		WHERE team = ? AND kind = ? AND principal LIKE ? AND (revoked_at IS NULL OR revoked_at > ?)`,
		at, string(t.team), TokenKindRunner, like, at); err != nil {
		return nil, err
	}
	return revoked, tx.Commit()
}

func livePrefixesLike(ctx context.Context, tx *storeTx, team Team, like string, at int64) (_ []string, err error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT prefix FROM tokens
		WHERE team = ? AND kind = ? AND principal LIKE ? AND (revoked_at IS NULL OR revoked_at > ?)`,
		string(team), TokenKindRunner, like, at)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	var out []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			return nil, err
		}
		out = append(out, prefix)
	}
	return out, rows.Err()
}

// MaxGitHubRunnerCredentials bounds the live credentials one team's GitHub
// Actions jobs hold at once, since each exchange is unauthenticated until
// the job's ID token verifies.
const MaxGitHubRunnerCredentials = 20

// ErrGitHubRunnerCredentialLimit is returned when a team already holds
// [MaxGitHubRunnerCredentials] live GitHub Actions credentials.
var ErrGitHubRunnerCredentialLimit = errors.New("store: this team already holds the maximum live GitHub Actions runner credentials")

// GitHubRunnerPush is the push a GitHub Actions job's ID token was issued
// for: a branch and the commit on it.
type GitHubRunnerPush struct {
	Branch string
	SHA    string
}

// MintGitHubRunnerCredential mints a runner credential for principal bound to
// push and the live binding, valid for ttl. It refuses with
// [ErrGitHubRunnerCredentialLimit] once
// the team holds [MaxGitHubRunnerCredentials] live ones. The count and the
// mint share a transaction under the team's lock, and the binding row is
// locked before minting, so concurrent exchanges cannot pass the limit and
// an unbind cannot leave a usable credential behind. It drops the team's
// expired and revoked GitHub Actions credentials first.
func (t *Tenant) MintGitHubRunnerCredential(
	ctx context.Context, binding GitHubRunnerBinding, principal string, push GitHubRunnerPush, scopes []string, ttl time.Duration, now time.Time,
) (string, *Token, error) {
	if binding.Team != t.team || binding.RepositoryID <= 0 || binding.RepositoryOwnerID <= 0 ||
		!strings.HasPrefix(principal, GitHubRunnerPrincipalPrefix+fmt.Sprint(binding.RepositoryID)+":") ||
		push.Branch == "" || push.SHA == "" || ttl <= 0 {
		return "", nil, ErrInvalidInput
	}
	if err := refuseAdminScope(scopes); err != nil {
		return "", nil, err
	}
	for attempt := 1; ; attempt++ {
		raw, tok, err := t.mintGitHubRunnerCredentialOnce(ctx, binding, principal, push, scopes, ttl, now)
		if err == nil {
			return raw, tok, nil
		}
		if attempt < mintAttempts && isTokenPrefixCollision(err) {
			continue
		}
		return "", nil, err
	}
}

func (t *Tenant) mintGitHubRunnerCredentialOnce(
	ctx context.Context, binding GitHubRunnerBinding, principal string, push GitHubRunnerPush, scopes []string, ttl time.Duration, now time.Time,
) (string, *Token, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return "", nil, err
	}
	defer rollbackOrLog(tx)
	if err := t.lockTeamTx(ctx, tx); err != nil {
		return "", nil, err
	}
	var ownerID int64
	err = tx.QueryRowContext(ctx, `SELECT repository_owner_id FROM github_runner_bindings
		WHERE team = ? AND repository_id = ?`+tx.forUpdate(), string(t.team), binding.RepositoryID).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && ownerID != binding.RepositoryOwnerID) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	at := now.UTC().Unix()
	like := GitHubRunnerPrincipalPrefix + "%"
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM github_runner_credentials WHERE team = ? AND expires_at <= ?`, string(t.team), at); err != nil {
		return "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM tokens
		WHERE team = ? AND kind = ? AND principal LIKE ?
		  AND ((expires_at IS NOT NULL AND expires_at <= ?) OR (revoked_at IS NOT NULL AND revoked_at <= ?))`,
		string(t.team), TokenKindRunner, like, at, at); err != nil {
		return "", nil, err
	}
	var live int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tokens
		WHERE team = ? AND kind = ? AND principal LIKE ?
		  AND (revoked_at IS NULL OR revoked_at > ?) AND expires_at IS NOT NULL AND expires_at > ?`,
		string(t.team), TokenKindRunner, like, at, at).Scan(&live); err != nil {
		return "", nil, err
	}
	if live >= MaxGitHubRunnerCredentials {
		return "", nil, ErrGitHubRunnerCredentialLimit
	}
	raw, tok, err := createTokenRow(ctx, tx, t.team, principal, TokenKindRunner, scopes, ttl, now, TokenOptions{})
	if err != nil {
		return "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO github_runner_credentials (team, prefix, branch, sha, expires_at) VALUES (?, ?, ?, ?, ?)`,
		string(t.team), tok.Prefix, push.Branch, push.SHA, tok.ExpiresAt.UTC().Unix()); err != nil {
		return "", nil, err
	}
	return raw, tok, tx.Commit()
}

// GitHubRunnerCredentialPush returns the push the GitHub Actions credential
// with prefix was minted for, or [ErrNotFound].
func (t *Tenant) GitHubRunnerCredentialPush(ctx context.Context, prefix string) (GitHubRunnerPush, error) {
	var push GitHubRunnerPush
	err := t.s.queryRow(ctx,
		`SELECT branch, sha FROM github_runner_credentials WHERE team = ? AND prefix = ?`,
		string(t.team), prefix).Scan(&push.Branch, &push.SHA)
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubRunnerPush{}, ErrNotFound
	}
	return push, err
}

// GitHubRepo names a repository on github.com.
type GitHubRepo struct {
	Owner string
	Name  string
}

// ParseGitHubRepo reads "owner/name". Both parts are GitHub's character set:
// letters, digits, '-', '_' and '.'.
func ParseGitHubRepo(slug string) (GitHubRepo, bool) {
	owner, name, ok := strings.Cut(strings.TrimSpace(slug), "/")
	if !ok || !githubNamePart(owner) || !githubNamePart(name) || name == "." || name == ".." {
		return GitHubRepo{}, false
	}
	return GitHubRepo{Owner: owner, Name: name}, true
}

func githubNamePart(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

// Slug is "owner/name".
func (r GitHubRepo) Slug() string { return r.Owner + "/" + r.Name }

// CanonicalURL is the repository's https URL, lowercased, because GitHub
// resolves owner and name case-insensitively.
func (r GitHubRepo) CanonicalURL() string {
	return "https://github.com/" + strings.ToLower(r.Slug())
}

// spellings lists, lowercased, every way a trigger's repo or repo_url field
// can name r: the slug, and the https, ssh and scp-style clone URLs with and
// without ".git".
func (r GitHubRepo) spellings() []string {
	slug := strings.ToLower(r.Slug())
	out := []string{slug}
	for _, base := range []string{"https://github.com/", "http://github.com/", "github.com/", "ssh://git@github.com/", "git@github.com:"} {
		out = append(out, base+slug, base+slug+".git")
	}
	return out
}

// TriggerNamesGitHubRepo reports whether tr is work of repo: every repository
// field tr carries names repo, and its GitHub owner and name, which every
// GitHub-origin trigger records, do.
func TriggerNamesGitHubRepo(tr *Trigger, repo GitHubRepo) bool {
	if tr == nil {
		return false
	}
	if !strings.EqualFold(tr.GithubOwner, repo.Owner) || !strings.EqualFold(tr.GithubRepo, repo.Name) {
		return false
	}
	names := func(v string) bool {
		v = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(v), "/"))
		for _, s := range repo.spellings() {
			if v == s {
				return true
			}
		}
		return false
	}
	if tr.Repo != "" && !names(tr.Repo) {
		return false
	}
	if tr.RepoURL != "" && !names(tr.RepoURL) {
		return false
	}
	// safety: a worker clones GITHUB_REPOSITORY in preference to the other
	// fields, so a trigger naming another repository there is another
	// repository's work.
	if env := tr.TriggerEnv["GITHUB_REPOSITORY"]; env != "" && !names(env) {
		return false
	}
	return true
}

// GitHubRunnerScope confines a claim to one team's work for one push of one
// repository. It is the claim restriction of a credential a GitHub Actions
// job exchanged its ID token for.
type GitHubRunnerScope struct {
	Team Team
	Repo GitHubRepo
	// Push is the branch and commit the job's ID token names; the scope
	// admits only triggers recorded for exactly that branch and commit.
	Push GitHubRunnerPush
}

// admits reports whether tr is work of the scope's repository recorded for
// the scope's push. tr's team is checked by the caller's query.
func (g GitHubRunnerScope) admits(tr *Trigger) bool {
	return g.Push.Branch != "" && g.Push.SHA != "" &&
		tr.TriggerSource == "github" && tr.TriggerEnv["GITHUB_EVENT_NAME"] == "push" &&
		tr.WebhookDelivery != "" && tr.WebhookReplayKey != "" &&
		tr.RetryOf == "" && tr.ParentRunID == "" && tr.ParentNodeID == "" &&
		tr.GitBranch == g.Push.Branch && tr.GitSHA == g.Push.SHA && TriggerNamesGitHubRepo(tr, g.Repo)
}

type githubRunnerScopeKey struct{}

// WithGitHubRunnerScope confines the node and trigger claims made under ctx
// to scope.
func WithGitHubRunnerScope(ctx context.Context, scope GitHubRunnerScope) context.Context {
	return context.WithValue(ctx, githubRunnerScopeKey{}, scope)
}

// GitHubRunnerScopeFrom returns the scope [WithGitHubRunnerScope] set on ctx.
func GitHubRunnerScopeFrom(ctx context.Context) (GitHubRunnerScope, bool) {
	scope, ok := ctx.Value(githubRunnerScopeKey{}).(GitHubRunnerScope)
	return scope, ok
}

// triggerClause narrows a triggers query to rows the scope admits by their
// columns. trigger_env is a blob no dialect filters, so a caller checks it
// with [TriggerNamesGitHubRepo] on the row it reads.
func (g GitHubRunnerScope) triggerClause(alias string) (string, []any) {
	col := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	spellings := g.Repo.spellings()
	ph := strings.TrimSuffix(strings.Repeat("?,", len(spellings)), ",")
	clause := ` AND ` + col("team") + ` = ?` +
		` AND ` + col("trigger_source") + ` = 'github'` +
		` AND ` + col("webhook_delivery") + ` != '' AND ` + col("webhook_replay_key") + ` != ''` +
		` AND ` + col("retry_of") + ` = '' AND COALESCE(` + col("parent_run_id") + `, '') = '' AND ` + col("parent_node_id") + ` = ''` +
		` AND LOWER(` + col("github_owner") + `) = ? AND LOWER(` + col("github_repo") + `) = ?` +
		` AND (` + col("repo") + ` = '' OR LOWER(` + col("repo") + `) IN (` + ph + `))` +
		` AND (` + col("repo_url") + ` = '' OR LOWER(` + col("repo_url") + `) IN (` + ph + `))` +
		` AND ` + col("git_branch") + ` = ? AND ` + col("git_sha") + ` = ?`
	args := []any{string(g.Team), strings.ToLower(g.Repo.Owner), strings.ToLower(g.Repo.Name)}
	for range 2 {
		for _, s := range spellings {
			args = append(args, s)
		}
	}
	return clause, append(args, g.Push.Branch, g.Push.SHA)
}

// nodeClause narrows a nodes query to nodes of runs whose trigger the scope
// admits by its columns.
func (g GitHubRunnerScope) nodeClause() (string, []any) {
	inner, innerArgs := g.triggerClause("gt")
	return ` AND team = ? AND run_id IN (SELECT gt.id FROM triggers gt WHERE 1 = 1` + inner + `)`,
		append([]any{string(g.Team)}, innerArgs...)
}

// GitHubRunnerAdmits reports whether scope admits runID: the run's trigger
// belongs to the scope's team, was recorded for the scope's branch and
// commit, and names the scope's repository in every repository field,
// including the ones in its environment.
func (s *Store) GitHubRunnerAdmits(ctx context.Context, scope GitHubRunnerScope, runID string) (bool, error) {
	var tr Trigger
	var envJSON []byte
	err := s.queryRow(ctx, `
		SELECT repo, repo_url, github_owner, github_repo, trigger_env, git_branch, git_sha,
		       trigger_source, retry_of, COALESCE(parent_run_id, ''), parent_node_id,
		       webhook_delivery, webhook_replay_key
		FROM triggers WHERE team = ? AND id = ?`, string(scope.Team), runID).
		Scan(&tr.Repo, &tr.RepoURL, &tr.GithubOwner, &tr.GithubRepo, &envJSON, &tr.GitBranch, &tr.GitSHA,
			&tr.TriggerSource, &tr.RetryOf, &tr.ParentRunID, &tr.ParentNodeID, &tr.WebhookDelivery, &tr.WebhookReplayKey)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	env, decoded := decodeTriggerEnv(envJSON)
	if !decoded {
		return false, nil
	}
	tr.TriggerEnv = env
	return scope.admits(&tr), nil
}

// safety: an environment that does not decode cannot be shown to name the
// repository, so a caller refuses the trigger rather than skipping the check.
func decodeTriggerEnv(blob []byte) (map[string]string, bool) {
	if len(blob) == 0 {
		return nil, true
	}
	var env map[string]string
	return env, json.Unmarshal(blob, &env) == nil
}

func (g GitHubRunnerScope) admitsRun(ctx context.Context, s *Store, runID string, seen map[string]bool) (bool, error) {
	if ok, cached := seen[runID]; cached {
		return ok, nil
	}
	ok, err := s.GitHubRunnerAdmits(ctx, g, runID)
	if err != nil {
		return false, err
	}
	seen[runID] = ok
	return ok, nil
}
