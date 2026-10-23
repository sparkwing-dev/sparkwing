package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// RegisterGitHubRunner records repository consent under current team administration authority.
func (t *Tenant) RegisterGitHubRunner(ctx context.Context, a capability.Authorized, row GitHubRunnerBinding) (GitHubRunnerBinding, error) {
	var out GitHubRunnerBinding
	err := t.mutateCredentials(ctx, a, "RegisterGitHubRunner", row.Repository, func(tx *storeTx, actor string) error {
		repo, valid := ParseGitHubRepo(row.Repository)
		if !valid || row.RepositoryID <= 0 || row.RepositoryOwnerID <= 0 {
			return ErrInvalidInput
		}
		if row.Team != "" && row.Team != t.team || row.CreatedBy != "" && row.CreatedBy != actor {
			return bindingRefusal()
		}
		var count, exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN repository_id=? THEN 1 ELSE 0 END),0) FROM github_runner_bindings WHERE team=?`, row.RepositoryID, t.team).Scan(&count, &exists); err != nil {
			return err
		}
		if exists != 0 {
			return ErrAlreadyBound
		}
		if holdsFreeAllowance(t.team) && count >= MaxGitHubRunnerBindings {
			return ErrBindingLimit
		}
		out = GitHubRunnerBinding{Team: t.team, RepositoryID: row.RepositoryID, RepositoryOwnerID: row.RepositoryOwnerID, Repository: repo.Slug(), CreatedBy: actor, CreatedAt: tx.now(ctx)}
		_, err := tx.ExecContext(ctx, `INSERT INTO github_runner_bindings(team,repository_id,repository_owner_id,repository,created_by,created_at,repo_key) VALUES (?,?,?,?,?,?,?)`, t.team, out.RepositoryID, out.RepositoryOwnerID, out.Repository, actor, out.CreatedAt.UnixNano(), githubRepositoryKey(out.RepositoryID))
		return err
	})
	if err != nil {
		return GitHubRunnerBinding{}, err
	}
	return out, nil
}

// RevokeGitHubRunner removes enrollment and ends its Actions authority in one transaction.
func (t *Tenant) RevokeGitHubRunner(ctx context.Context, a capability.Authorized, repositoryID int64) error {
	if repositoryID <= 0 {
		return ErrInvalidInput
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		var prefixes []string
		if err := acquireLockSet(ctx, tx, func() lockSet {
			rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND bind_kind='push' AND repo_key=? AND parent_id IS NULL`, t.team, githubRepositoryKey(repositoryID))
			if err != nil {
				return lockSet{err: err}
			}
			prefixes, err = scanPrefixes(rows)
			if err != nil {
				return lockSet{err: err}
			}
			leaves := []capability.AuthorityRef{a.Principal.Authority}
			for _, prefix := range prefixes {
				leaves = append(leaves, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
			}
			set := discoverAuthorityLocks(ctx, tx, leaves, prefixes...)
			set.add(lockTeam, true, string(t.team))
			return set
		}); err != nil {
			return err
		}
		return t.teamMutationInTx(ctx, tx, a, "RevokeGitHubRunner", string(t.team), func(tx *storeTx, _ capability.Principal) error {
			result, err := tx.ExecContext(ctx, `DELETE FROM github_runner_bindings WHERE team=? AND repository_id=?`, t.team, repositoryID)
			if err := credentialRowChanged(result, err); err != nil {
				return err
			}
			for _, prefix := range prefixes {
				if err := tx.operator.revokeAuthorityTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func githubRepositoryKey(repositoryID int64) string {
	return "gh:" + strconv.FormatInt(repositoryID, 10)
}

func checkActionsEnrollmentTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec) error {
	push := spec.Binding.Push
	if push == nil {
		return bindingRefusal()
	}
	var repositoryID, ownerID int64
	err := tx.QueryRowContext(ctx, `SELECT repository_id,repository_owner_id FROM github_runner_bindings WHERE team=? AND repo_key=?`+tx.forShare(), spec.Team, push.RepositoryKey).Scan(&repositoryID, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return bindingRefusal()
	}
	if err != nil {
		return err
	}
	registration := "github:" + strconv.FormatInt(repositoryID, 10) + ":" + strconv.FormatInt(ownerID, 10)
	if spec.Team == "" || spec.Subject.Kind != capability.SubjectMachine || spec.Binding.Kind != capability.BindingPush || push.WorkflowRunID == "" || push.WorkflowRunAttempt == "" || push.Ref == "" || push.SHA == "" || !strings.HasSuffix(push.WorkflowRef, "@"+push.Ref) || !slices.Equal(spec.Repositories, []string{push.RepositoryKey}) || repositoryID <= 0 || ownerID <= 0 || push.RepositoryKey != githubRepositoryKey(repositoryID) || spec.RegistrationID != registration || spec.Subject.ID != registration+":"+push.WorkflowRunID+":"+push.WorkflowRunAttempt || spec.Authority != nil || spec.Metered || spec.Data != nil || len(spec.ReservedPools) != 0 ||
		len(spec.Grants) != 2 || !slices.Contains(spec.Grants, capability.NodesClaim) || !slices.Contains(spec.Grants, capability.TriggersClaim) {
		return bindingRefusal()
	}
	if spec.ExpiresAt == nil {
		return authorityError(apierr.Forbidden, "Actions credential requires its verified expiry ceiling")
	}
	var live int
	now := tx.now(ctx)
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM capabilities WHERE team=? AND bind_kind='push' AND parent_id IS NULL AND `+liveCredentialSQL(""), spec.Team, now.UnixNano(), now.UnixNano()).Scan(&live); err != nil {
		return err
	}
	if live >= MaxGitHubRunnerCredentials {
		return ErrGitHubRunnerCredentialLimit
	}
	return nil
}

func consumeActionsExchangeTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec) error {
	push := spec.Binding.Push
	result, err := tx.ExecContext(ctx, `INSERT INTO github_actions_exchanges(team,repository_key,run_id,run_attempt) VALUES (?,?,?,?) ON CONFLICT(team,repository_key,run_id,run_attempt) DO NOTHING`, spec.Team, push.RepositoryKey, push.WorkflowRunID, push.WorkflowRunAttempt)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return bindingRefusal()
	}
	return nil
}
