package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// ActionsApproval permits one workflow ref and hosted image in a bound repository.
type ActionsApproval struct {
	RepositoryID int64  `json:"repository_id"`
	Workflow     string `json:"workflow"`
	Ref          string `json:"ref"`
	Runner       string `json:"runner"`
}

func newActionsReceipt() (string, error) {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

// PutActionsApproval records team owner consent. Replacement fences earlier receipts.
func (t *Tenant) PutActionsApproval(ctx context.Context, a capability.Authorized, approval ActionsApproval, remove bool) error {
	tier := runspec.RepositoryPlacement{Prefer: []runspec.RepositoryTier{{Target: runspec.PlacementGitHubActions, Workflow: approval.Workflow, Runner: approval.Runner}}}
	if err := runspec.ValidateRepositoryTiers(tier.Prefer); err != nil {
		return err
	}
	dir, file := path.Split(approval.Workflow)
	if dir != ".github/workflows/" || path.Clean(approval.Workflow) != approval.Workflow || !strings.HasSuffix(file, ".yaml") && !strings.HasSuffix(file, ".yml") || strings.ContainsAny(approval.Runner, "/\\ \t\r\n") {
		return ErrInvalidInput
	}
	if approval.RepositoryID <= 0 || !remove && (!strings.HasPrefix(approval.Ref, "refs/heads/") || strings.ContainsAny(approval.Ref, " \t\r\n")) {
		return ErrInvalidInput
	}
	return t.teamMutationTx(ctx, a, "PutActionsApproval", string(t.team), func(tx *storeTx, _ capability.Principal) error {
		var current string
		err := tx.QueryRowContext(ctx, `SELECT workflow_ref FROM github_actions_approvals WHERE team=? AND repository_id=? AND workflow=? AND runner=?`, t.team, approval.RepositoryID, approval.Workflow, approval.Runner).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !remove && err == nil && current == approval.Ref {
			return nil
		}
		id, err := newActionsReceipt()
		if err != nil {
			return err
		}
		if _, err = tx.execContext(ctx, `UPDATE github_actions_dispatches SET state='cancelled',reason='workflow approval changed' WHERE team=? AND repository_id=? AND workflow=? AND runner=?`, t.team, approval.RepositoryID, approval.Workflow, approval.Runner); err != nil {
			return err
		}
		if remove {
			_, err = tx.execContext(ctx, `DELETE FROM github_actions_approvals WHERE team=? AND repository_id=? AND workflow=? AND runner=?`, t.team, approval.RepositoryID, approval.Workflow, approval.Runner)
			return err
		}
		_, err = tx.execContext(ctx, `INSERT INTO github_actions_approvals(team,repository_id,workflow,runner,workflow_ref,approval_id) VALUES(?,?,?,?,?,?) ON CONFLICT(team,repository_id,workflow,runner) DO UPDATE SET workflow_ref=excluded.workflow_ref,approval_id=excluded.approval_id`, t.team, approval.RepositoryID, approval.Workflow, approval.Runner, approval.Ref, id)
		return err
	})
}

// ActionsDispatch is a non-bearer receipt for one job placement generation.
type ActionsDispatch struct {
	Receipt                                                                string
	Team                                                                   Team
	RunID, JobID                                                           string
	QueueStartedAt, Generation                                             int64
	ApprovalID                                                             string
	InstallationID, RepositoryID, RepositoryOwnerID                        int64
	Repository, Workflow, WorkflowRef, WorkflowSHA, Runner, PolicyRevision string
	State, Reason, ProviderRunID, ProviderAttempt                          string
}

const actionsDispatchColumns = `receipt,team,run_id,node_id,queue_started_at,generation,approval_id,installation_id,repository_id,repository_owner_id,repository,workflow,workflow_ref,workflow_sha,runner,policy_revision,state,reason,provider_run_id,provider_attempt`

func scanActionsDispatch(row interface{ Scan(...any) error }) (d ActionsDispatch, err error) {
	err = row.Scan(&d.Receipt, &d.Team, &d.RunID, &d.JobID, &d.QueueStartedAt, &d.Generation, &d.ApprovalID, &d.InstallationID, &d.RepositoryID, &d.RepositoryOwnerID, &d.Repository, &d.Workflow, &d.WorkflowRef, &d.WorkflowSHA, &d.Runner, &d.PolicyRevision, &d.State, &d.Reason, &d.ProviderRunID, &d.ProviderAttempt)
	return d, err
}

// ReadActionsDispatch reads a receipt for authenticated provider registration.
func (op *Operator) ReadActionsDispatch(ctx context.Context, team Team, receipt string) (ActionsDispatch, error) {
	d, err := scanActionsDispatch(op.s.queryRow(ctx, `SELECT `+actionsDispatchColumns+` FROM github_actions_dispatches WHERE team=? AND receipt=?`, team, receipt))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return d, err
}

// safety: approval identity prevents revoke/reapprove from reviving an old receipt; live installation and
// repository consent are checked again at send, exchange and claim, rather than trusting intake-time consent.
const actionsConsentSQL = `EXISTS (SELECT 1 FROM github_actions_approvals a JOIN github_runner_bindings b ON b.team=a.team AND b.repository_id=a.repository_id JOIN github_app_installations i ON i.team=b.team AND i.installation_id=b.installation_id WHERE a.approval_id=d.approval_id AND a.team=d.team AND a.repository_id=d.repository_id AND b.installation_id=d.installation_id AND b.repository_owner_id=d.repository_owner_id AND i.suspended=0) AND (EXISTS (SELECT 1 FROM github_app_automation a WHERE a.team=d.team AND a.installation_id=d.installation_id AND a.repository_id=d.repository_id) OR EXISTS (SELECT 1 FROM github_app_triggers t JOIN runs r ON r.team=d.team AND r.id=d.run_id WHERE t.team=d.team AND t.installation_id=d.installation_id AND t.repository_id=d.repository_id AND t.pipeline=r.pipeline))`

// NextActionsDispatch creates an intent under claim arbitration without reserving the job.
func (op *Operator) NextActionsDispatch(ctx context.Context) (*ActionsDispatch, error) {
	var out *ActionsDispatch
	err := op.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockClaimQueueTx(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.execContext(ctx, `UPDATE github_actions_dispatches SET state='uncertain',reason='sender stopped before its outcome was recorded' WHERE state='sending' AND send_until<=?`, tx.now(ctx).UnixNano()); err != nil {
			return err
		}
		pending, err := scanActionsDispatch(tx.QueryRowContext(ctx, `SELECT `+actionsDispatchColumns+` FROM github_actions_dispatches WHERE state='requested' ORDER BY receipt LIMIT 1`))
		if err == nil {
			out = &pending
			return takeActionsSendTx(ctx, tx, pending.Receipt)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		q := claimSQL{dialect: tx.dialect}
		rows, err := tx.queryContext(ctx, `SELECT team,id FROM runs r WHERE status IN ('pending','running') AND cancel_requested_at IS NULL AND dispatch='controller' AND `+q.at(q.blob("r.request_json"), "request", "placement")+` IS NOT NULL AND EXISTS (SELECT 1 FROM nodes n WHERE n.team=r.team AND n.run_id=r.id AND n.status='pending' AND n.ready_at IS NOT NULL AND n.claimed_by IS NULL AND NOT EXISTS (SELECT 1 FROM github_actions_dispatches d WHERE d.team=n.team AND d.run_id=n.run_id AND d.node_id=n.node_id AND d.queue_started_at=COALESCE(n.queue_started_at,n.ready_at))) ORDER BY created_at,id`)
		if err != nil {
			return err
		}
		type key struct {
			team Team
			run  string
		}
		var keys []key
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.team, &k.run); err != nil {
				return errors.Join(err, rows.Close())
			}
			keys = append(keys, k)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, k := range keys {
			run, err := readRunTx(ctx, tx, k.team, k.run)
			if err != nil {
				return err
			}
			if !run.Request.Request.Placement.Active() || actionsForkWork(run) {
				continue
			}
			waits, err := readPlacementWaitsTx(ctx, tx, k.team, run)
			if err != nil {
				return err
			}
			for _, wait := range waits {
				for _, tier := range wait.Tiers {
					if tier.Tier.Target != runspec.PlacementGitHubActions || tier.OpensIn > 0 || tier.Unavailable == "off in this team's runner ranking" {
						continue
					}
					var exists int
					if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM github_actions_dispatches WHERE team=? AND run_id=? AND node_id=? AND queue_started_at=?`, k.team, k.run, wait.JobID, wait.ReadyAt.UnixNano()).Scan(&exists); err != nil {
						return err
					}
					if exists > 0 {
						continue
					}
					d := ActionsDispatch{Team: k.team, RunID: k.run, JobID: wait.JobID, QueueStartedAt: wait.ReadyAt.UnixNano(), Generation: 1, Workflow: tier.Tier.Workflow, Runner: tier.Tier.Runner, PolicyRevision: wait.Revision, State: "requested"}
					err = tx.QueryRowContext(ctx, `SELECT a.approval_id,a.workflow_ref,b.repository_id,b.repository_owner_id,b.repository,b.installation_id FROM github_actions_approvals a JOIN github_runner_bindings b ON b.team=a.team AND b.repository_id=a.repository_id WHERE a.team=? AND b.repo_key=? AND a.workflow=? AND a.runner=?`, k.team, run.Request.Request.Git.Repository.Key, d.Workflow, d.Runner).Scan(&d.ApprovalID, &d.WorkflowRef, &d.RepositoryID, &d.RepositoryOwnerID, &d.Repository, &d.InstallationID)
					if errors.Is(err, sql.ErrNoRows) {
						continue
					}
					if err != nil {
						return err
					}
					if err := tx.QueryRowContext(ctx, `SELECT approval_id,workflow_ref FROM github_actions_approvals WHERE team=? AND repository_id=? AND workflow=? AND runner=?`, d.Team, d.RepositoryID, d.Workflow, d.Runner).Scan(&d.ApprovalID, &d.WorkflowRef); err != nil {
						return err
					}
					d.Receipt, err = newActionsReceipt()
					if err != nil {
						return err
					}
					if err = insertActionsDispatchTx(ctx, tx, d); err != nil {
						return err
					}
					out = &d
					return takeActionsSendTx(ctx, tx, d.Receipt)
				}
			}
		}
		return nil
	})
	return out, err
}

func insertActionsDispatchTx(ctx context.Context, tx *storeTx, d ActionsDispatch) error {
	_, err := tx.execContext(ctx, `INSERT INTO github_actions_dispatches(`+actionsDispatchColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, d.Receipt, d.Team, d.RunID, d.JobID, d.QueueStartedAt, d.Generation, d.ApprovalID, d.InstallationID, d.RepositoryID, d.RepositoryOwnerID, d.Repository, d.Workflow, d.WorkflowRef, d.WorkflowSHA, d.Runner, d.PolicyRevision, d.State, d.Reason, d.ProviderRunID, d.ProviderAttempt)
	return err
}

// BeginActionsDispatch binds the workflow revision and rechecks claims, cancellation and approval before HTTP.
func (op *Operator) BeginActionsDispatch(ctx context.Context, d ActionsDispatch, sha string) (bool, error) {
	var started bool
	if !runspec.IsCommitID(sha) {
		return false, ErrInvalidInput
	}
	err := op.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockClaimQueueTx(ctx, tx); err != nil {
			return err
		}
		result, err := tx.execContext(ctx, `UPDATE github_actions_dispatches AS d SET state='sending',workflow_sha=?,send_until=? WHERE receipt=? AND state='sending' AND workflow_sha='' AND send_until>? AND `+actionsConsentSQL+` AND EXISTS (SELECT 1 FROM nodes n JOIN runs r ON r.team=n.team AND r.id=n.run_id WHERE n.team=d.team AND n.run_id=d.run_id AND n.node_id=d.node_id AND COALESCE(n.queue_started_at,n.ready_at)=d.queue_started_at AND n.status='pending' AND n.claimed_by IS NULL AND n.capability_prefix='' AND n.cancel_requested_at IS NULL AND r.cancel_requested_at IS NULL AND r.status IN ('pending','running'))`, sha, tx.now(ctx).Add(time.Minute).UnixNano(), d.Receipt, tx.now(ctx).UnixNano())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		started = n == 1
		return err
	})
	return started, err
}

// FinishActionsDispatch stores the outcome without replaying a sending generation.
func (op *Operator) FinishActionsDispatch(ctx context.Context, receipt, state, reason string) error {
	if !slices.Contains([]string{"accepted", "refused", "uncertain"}, state) {
		return ErrInvalidInput
	}
	_, err := op.s.exec(ctx, `UPDATE github_actions_dispatches SET state=?,reason=? WHERE receipt=? AND state IN ('requested','sending','uncertain')`, state, reason, receipt)
	return err
}

// PendingActionsCancellations invalidates receipts before asking the provider to cancel known runs.
// A job that finished without a cancellation request leaves its workflow to exit on its own,
// so GitHub reports that run's real outcome instead of a cancellation.
func (op *Operator) PendingActionsCancellations(ctx context.Context) ([]ActionsDispatch, error) {
	var out []ActionsDispatch
	err := op.s.inTx(ctx, func(tx *storeTx) error {
		_, err := tx.execContext(ctx, `UPDATE github_actions_dispatches AS d SET state='cancelled',reason='job finished',cancel_sent=1 WHERE state<>'cancelled' AND EXISTS (SELECT 1 FROM nodes n WHERE n.team=d.team AND n.run_id=d.run_id AND n.node_id=d.node_id AND COALESCE(n.queue_started_at,n.ready_at)=d.queue_started_at AND n.status='done' AND n.outcome<>'cancelled' AND n.cancel_requested_at IS NULL)`)
		if err != nil {
			return err
		}
		_, err = tx.execContext(ctx, `UPDATE github_actions_dispatches AS d SET state='cancelled',reason='job ended or consent withdrawn' WHERE state<>'cancelled' AND (NOT (`+actionsConsentSQL+`) OR NOT EXISTS (SELECT 1 FROM nodes n JOIN runs r ON r.team=n.team AND r.id=n.run_id WHERE n.team=d.team AND n.run_id=d.run_id AND n.node_id=d.node_id AND COALESCE(n.queue_started_at,n.ready_at)=d.queue_started_at AND n.status IN ('pending','running') AND n.cancel_requested_at IS NULL AND r.cancel_requested_at IS NULL AND r.status IN ('pending','running')))`)
		if err != nil {
			return err
		}
		rows, err := tx.queryContext(ctx, `SELECT `+actionsDispatchColumns+` FROM github_actions_dispatches WHERE state='cancelled' AND provider_run_id<>'' AND cancel_sent=0`)
		if err != nil {
			return err
		}
		for rows.Next() {
			d, err := scanActionsDispatch(rows)
			if err != nil {
				return errors.Join(err, rows.Close())
			}
			out = append(out, d)
		}
		return errors.Join(rows.Err(), rows.Close())
	})
	return out, err
}

// FinishActionsCancellation records the provider's successful cancellation request.
func (op *Operator) FinishActionsCancellation(ctx context.Context, receipt string) error {
	_, err := op.s.exec(ctx, `UPDATE github_actions_dispatches SET cancel_sent=1 WHERE receipt=? AND state='cancelled'`, receipt)
	return err
}

func actionsReceiptScopeSQL(q *claimSQL, push, alias string) string {
	b := func(name string) string { return q.at(push, name) }
	return `EXISTS (SELECT 1 FROM github_actions_dispatches d WHERE d.receipt=` + b("receipt") + ` AND d.team=` + alias + `.team AND d.run_id=` + alias + `.run_id AND d.node_id=` + alias + `.node_id AND ` + alias + `.kind='node' AND d.state IN ('sending','accepted','uncertain') AND d.provider_run_id=` + b("workflow_run_id") + ` AND d.provider_attempt=` + b("workflow_run_attempt") + ` AND ` + actionsConsentSQL + ` AND EXISTS (SELECT 1 FROM nodes n WHERE n.team=d.team AND n.run_id=d.run_id AND n.node_id=d.node_id AND COALESCE(n.queue_started_at,n.ready_at)=d.queue_started_at))`
}

// RetryActionsDispatch invalidates a failed receipt before an audited operator opens a new generation.
func (op *Operator) RetryActionsDispatch(ctx context.Context, a capability.Authorized, team Team, receipt, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrInvalidInput
	}
	return op.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockClaimQueueTx(ctx, tx); err != nil {
			return err
		}
		p, err := tx.ResolveAuthority(ctx, a.Principal.Authority)
		if err != nil {
			return err
		}
		if !capability.DeploymentAuthority(p) || !slices.Contains(p.Capability.Grants, capability.OperatorLimits) {
			return bindingRefusal()
		}
		d, err := scanActionsDispatch(tx.QueryRowContext(ctx, `SELECT `+actionsDispatchColumns+` FROM github_actions_dispatches WHERE team=? AND receipt=?`, team, receipt))
		if err != nil {
			return err
		}
		if !slices.Contains([]string{"refused", "uncertain", "cancelled"}, d.State) || d.Reason == "operator retry superseded receipt" {
			return ErrInvalidInput
		}
		var available int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes n JOIN runs r ON r.team=n.team AND r.id=n.run_id WHERE n.team=? AND n.run_id=? AND n.node_id=? AND n.status='pending' AND n.claimed_by IS NULL AND COALESCE(n.queue_started_at,n.ready_at)=? AND n.cancel_requested_at IS NULL AND r.cancel_requested_at IS NULL AND r.status IN ('pending','running')`, team, d.RunID, d.JobID, d.QueueStartedAt).Scan(&available); err != nil {
			return err
		}
		if available != 1 {
			return bindingRefusal()
		}
		if _, err = tx.execContext(ctx, `UPDATE github_actions_dispatches SET state='cancelled',reason='operator retry superseded receipt' WHERE receipt=?`, receipt); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT approval_id,workflow_ref FROM github_actions_approvals WHERE team=? AND repository_id=? AND workflow=? AND runner=?`, d.Team, d.RepositoryID, d.Workflow, d.Runner).Scan(&d.ApprovalID, &d.WorkflowRef); err != nil {
			return err
		}
		d.Receipt, err = newActionsReceipt()
		if err != nil {
			return err
		}
		d.Generation++
		d.State = "requested"
		d.Reason = ""
		d.WorkflowSHA = ""
		d.ProviderRunID = ""
		d.ProviderAttempt = ""
		if err := insertActionsDispatchTx(ctx, tx, d); err != nil {
			return err
		}
		return tx.operator.recordAudit(ctx, tx, auditContext{Team: team, At: tx.now(ctx), Actor: auditPrincipalActor(p)}, auditMutation{Operation: "RetryActionsDispatch", SubjectID: receipt, Reason: new(digestAudit(reason))})
	})
}

// perf: the exchange refuses fork work, so dispatching it would only spend the repository's GitHub minutes.
func actionsForkWork(run RunRow) bool {
	pr := run.Request.Request.PullRequest
	return pr.Set && pr.Value.Fork
}

func actionsImageOS(image string) string {
	for prefix, system := range map[string]string{"ubuntu-": "linux", "macos-": "darwin", "windows-": "windows"} {
		if strings.HasPrefix(image, prefix) {
			return system
		}
	}
	return ""
}

func checkReceiptEnrollmentTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec) error {
	p := spec.Binding.Push
	d, err := scanActionsDispatch(tx.QueryRowContext(ctx, `SELECT `+actionsDispatchColumns+` FROM github_actions_dispatches d WHERE receipt=? AND `+actionsConsentSQL, p.Receipt))
	if err != nil {
		return bindingRefusal()
	}
	run, err := readRunTx(ctx, tx, d.Team, d.RunID)
	if err != nil {
		return err
	}
	source := run.Request.Provenance
	registration := fmtActionsRegistration(d.RepositoryID, d.RepositoryOwnerID)
	if !runspec.IsCommitID(p.WorkflowSHA) || spec.Team != string(d.Team) || spec.Binding.Kind != capability.BindingPush || spec.Subject != (capability.Subject{Kind: capability.SubjectMachine, ID: registration + ":" + p.WorkflowRunID + ":" + p.WorkflowRunAttempt}) || spec.RegistrationID != registration || spec.Authority != nil || spec.Metered || spec.Data != nil || len(spec.ReservedPools) != 0 || !slices.Equal(spec.Grants, []capability.Grant{capability.NodesClaim}) || !slices.Equal(spec.Repositories, []string{p.RepositoryKey}) || spec.ExpiresAt == nil || p.RepositoryKey != githubRepositoryKey(d.RepositoryID) || p.Repository != d.Repository || p.RunID != d.RunID || p.JobID != d.JobID || p.Ref != source.Ref() || p.SHA != source.SHA() || p.WorkflowRef != d.Repository+"/"+d.Workflow+"@"+d.WorkflowRef || p.WorkflowSHA != d.WorkflowSHA || p.RunnerImage != d.Runner || p.RunnerEnvironment != githubHostedRunner || p.WorkflowRunID == "" || p.WorkflowRunAttempt != "1" || !slices.Contains([]string{"sending", "accepted", "uncertain"}, d.State) || d.ProviderRunID != "" {
		return bindingRefusal()
	}
	var ready int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes n JOIN runs r ON r.team=n.team AND r.id=n.run_id WHERE n.team=? AND n.run_id=? AND n.node_id=? AND COALESCE(n.queue_started_at,n.ready_at)=? AND n.status='pending' AND n.cancel_requested_at IS NULL AND r.cancel_requested_at IS NULL AND r.status IN ('pending','running')`, d.Team, d.RunID, d.JobID, d.QueueStartedAt).Scan(&ready)
	if err != nil {
		return err
	}
	if ready != 1 {
		return bindingRefusal()
	}
	return nil
}

func fmtActionsRegistration(repository, owner int64) string {
	return "github:" + strconv.FormatInt(repository, 10) + ":" + strconv.FormatInt(owner, 10)
}

// FinishSkippedActionsDispatch retires an intent when another claimant won before sending.
func (op *Operator) FinishSkippedActionsDispatch(ctx context.Context, receipt string) error {
	_, err := op.s.exec(ctx, `UPDATE github_actions_dispatches SET state='cancelled',reason='another claimant won before dispatch' WHERE receipt=? AND state='sending' AND workflow_sha=''`, receipt)
	return err
}

func actionsPlacementWaitTx(ctx context.Context, tx *storeTx, team Team, run RunRow, wait *PlacementWait) error {
	for i := range wait.Tiers {
		tier := &wait.Tiers[i]
		if tier.Tier.Target != runspec.PlacementGitHubActions || tier.Unavailable == "off in this team's runner ranking" {
			continue
		}
		d, err := scanActionsDispatch(tx.QueryRowContext(ctx, `SELECT `+actionsDispatchColumns+` FROM github_actions_dispatches WHERE team=? AND run_id=? AND node_id=? AND queue_started_at=? ORDER BY generation DESC LIMIT 1`, team, run.ID, wait.JobID, wait.ReadyAt.UnixNano()))
		if err == nil {
			tier.Unavailable = ""
			tier.Dispatch = &d
			if d.State == "refused" || d.State == "cancelled" {
				tier.Unavailable = d.Reason
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if actionsForkWork(run) {
			tier.Unavailable = "fork pull requests cannot use the GitHub Actions fallback"
			continue
		}
		var approved int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM github_actions_approvals a JOIN github_runner_bindings b ON b.team=a.team AND b.repository_id=a.repository_id WHERE a.team=? AND b.repo_key=? AND a.workflow=? AND a.runner=?`, team, run.Request.Request.Git.Repository.Key, tier.Tier.Workflow, tier.Tier.Runner).Scan(&approved)
		if err != nil {
			return err
		}
		tier.OnDemand = approved == 1
		tier.Unavailable = ""
		if approved == 0 {
			tier.Unavailable = "a team owner must approve the workflow ref and hosted image through /api/v1/team/github-app/actions-approval"
		}
	}
	return nil
}

func takeActionsSendTx(ctx context.Context, tx *storeTx, receipt string) error {
	_, err := tx.execContext(ctx, `UPDATE github_actions_dispatches SET state='sending',send_until=? WHERE receipt=? AND state='requested'`, tx.now(ctx).Add(5*time.Minute).UnixNano(), receipt)
	return err
}

func invalidateActionsRepositoryTx(ctx context.Context, tx *storeTx, team Team, repository int64) error {
	_, err := tx.execContext(ctx, `UPDATE github_actions_dispatches AS d SET state='cancelled',reason='repository consent withdrawn' WHERE team=? AND repository_id=? AND NOT (`+actionsConsentSQL+`)`, team, repository)
	return err
}
