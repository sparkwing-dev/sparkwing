package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// RegisterGitHubRunner records repository consent under current team administration authority.
func (t *Tenant) RegisterGitHubRunner(ctx context.Context, a capability.Authorized, row GitHubRunnerBinding) (GitHubRunnerBinding, error) {
	var out GitHubRunnerBinding
	err := t.mutateCredentials(ctx, a, "RegisterGitHubRunner", row.Repository, func(tx *storeTx, actor string) error {
		repo, valid := ParseGitHubRepo(row.Repository)
		if !valid || row.RepositoryID <= 0 || row.RepositoryOwnerID <= 0 || row.InstallationID < 0 {
			return ErrInvalidInput
		}
		if row.Team != "" && row.Team != t.team || row.CreatedBy != "" && row.CreatedBy != actor {
			return bindingRefusal()
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM github_runner_bindings WHERE team=? AND repository_id=?`, t.team, row.RepositoryID).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return ErrAlreadyBound
		}
		out = GitHubRunnerBinding{Team: t.team, RepositoryID: row.RepositoryID, RepositoryOwnerID: row.RepositoryOwnerID, Repository: repo.Slug(), InstallationID: row.InstallationID, CreatedBy: actor, CreatedAt: tx.now(ctx)}
		_, err := tx.execContext(ctx, `INSERT INTO github_runner_bindings(team,repository_id,repository_owner_id,repository,installation_id,created_by,created_at,repo_key) VALUES (?,?,?,?,NULLIF(?,0),?,?,?)`, t.team, out.RepositoryID, out.RepositoryOwnerID, out.Repository, out.InstallationID, actor, out.CreatedAt.UnixNano(), githubRepositoryKey(out.RepositoryID))
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
			rows, err := tx.queryContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND bind_kind='push' AND repo_key=? AND parent_id IS NULL`, t.team, githubRepositoryKey(repositoryID))
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
			result, err := tx.execContext(ctx, `DELETE FROM github_runner_bindings WHERE team=? AND repository_id=?`, t.team, repositoryID)
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
	if push.Receipt != "" {
		return checkReceiptEnrollmentTx(ctx, tx, spec)
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
		return authorityError(hosterr.Forbidden, "Actions credential requires its verified expiry ceiling")
	}
	return nil
}

func consumeActionsExchangeTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec) error {
	push := spec.Binding.Push
	if push.Receipt != "" {
		result, err := tx.execContext(ctx, `UPDATE github_actions_dispatches AS d SET provider_run_id=?,provider_attempt=?,state='accepted' WHERE team=? AND receipt=? AND provider_run_id='' AND state IN ('sending','accepted','uncertain') AND `+actionsConsentSQL, push.WorkflowRunID, push.WorkflowRunAttempt, spec.Team, push.Receipt)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return actionsExchangeUsed()
		}
	}
	result, err := tx.execContext(ctx, `INSERT INTO github_actions_exchanges(team,repository_key,run_id,run_attempt) VALUES (?,?,?,?) ON CONFLICT(team,repository_key,run_id,run_attempt) DO NOTHING`, spec.Team, push.RepositoryKey, push.WorkflowRunID, push.WorkflowRunAttempt)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return actionsExchangeUsed()
	}
	return nil
}

func actionsExchangeUsed() error {
	refusal := hosterr.Authorization(hosterr.ReasonExchangeUsed, "this GitHub Actions run attempt already exchanged its token; each run attempt gets one runner credential")
	refusal.Hint = "Re-run the job for a new run attempt. If the runner restarted to update itself, set github.com/sparkwing-dev/sparkwing in .sparkwing/go.mod to the latest release."
	return refusal
}
