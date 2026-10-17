package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type admittedSnapshot struct {
	request runspec.SealedRequest
	spec    *runspec.Spec
	plan    json.RawMessage
	lineage runspec.Lineage
	retry   *RetrySelection
}

func admitRun(ctx context.Context, tx *storeTx, team Team, request IntakeRequest, now time.Time) (AdmissionResult, error) {
	principal, err := intakeAuthorityTx(ctx, tx, team, request)
	if err != nil {
		return AdmissionResult{}, err
	}
	snapshot, err := retryIntakeTx(ctx, tx, team, request, principal)
	if err != nil {
		return AdmissionResult{}, err
	}
	var identity any = struct {
		Request  runspec.Request
		Delivery string
	}{request.Request, request.VerifiedDeliveryID}
	if snapshot.retry != nil {
		identity = struct{ SourceRunID, Mode string }{snapshot.lineage.RetryOf, snapshot.retry.Mode}
	}
	digest, err := tx.store.planServices().CanonicalHash(identity)
	if err != nil {
		return AdmissionResult{}, err
	}
	pipeline := request.Request.Pipeline
	if snapshot.request.RunID != "" {
		pipeline = snapshot.request.Request.Pipeline
	}
	scope, err := intakeScope(principal, pipeline)
	if err != nil {
		return AdmissionResult{}, err
	}

	// safety: serializing intake per team reserves the number without taking the team row before owner authority.
	if err = advisoryLockTx(ctx, tx, "intake:"+string(team)); err != nil {
		return AdmissionResult{}, err
	}
	if request.IdempotencyKey != "" {
		replay, found, err := replayAdmissionTx(ctx, tx, team, scope, request.IdempotencyKey, string(digest))
		if err == nil && found {
			_, err = intakeAuthorityTx(ctx, tx, team, request)
		}
		if err != nil || found {
			return replay, err
		}
	}
	if tx.store.intakePolicy == nil {
		return AdmissionResult{}, authorityError(apierr.Unavailable, "intake policy is not configured")
	}
	if snapshot.retry != nil {
		if err := selectRetryTx(ctx, tx, team, snapshot.lineage.RetryOf, snapshot.retry); err != nil {
			return AdmissionResult{}, err
		}
	}
	policy, err := tx.store.intakePolicy.ResolveIntake(ctx, tx, request)
	if err != nil {
		return AdmissionResult{}, err
	}
	if policy.LogRetentionClass == "" || policy.LogRetentionDays <= 0 {
		return AdmissionResult{}, authorityError(apierr.Unavailable, "intake log retention policy is missing")
	}
	id, err := newCreditID("run")
	if err != nil {
		return AdmissionResult{}, err
	}
	if snapshot.request.RunID == "" {
		input, err := trustedIntakeRequest(request, principal, team, policy)
		if err != nil {
			return AdmissionResult{}, err
		}
		if replayKey := appReplayKey(input); input.Mode == runspec.IntakeExecute && replayKey != "" {
			var previous string
			err := tx.QueryRowContext(ctx, `SELECT id FROM triggers WHERE team=? AND webhook_replay_key<>'' AND webhook_replay_key=?`, team, replayKey).Scan(&previous)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return AdmissionResult{}, err
			}
			if err == nil {
				if _, err := intakeAuthorityTx(ctx, tx, team, request); err != nil {
					return AdmissionResult{}, err
				}
				return admissionResultTx(ctx, tx, team, previous)
			}
		}
		if err := validateChildLineageTx(ctx, tx, team, input); err != nil {
			return AdmissionResult{}, err
		}
		pools, err := intakePoolsTx(ctx, tx, team, &input, policy.RepositoryAuthorized)
		if err != nil {
			return AdmissionResult{}, err
		}
		snapshot.request, err = tx.store.intakeServices().SealRequest(input, runspec.IntakeFacts{RunID: id, AdmittedAt: now, AllowedPools: pools, AuthorityRef: request.Authority, Ceilings: policy.Ceilings})
		if err != nil {
			return AdmissionResult{}, err
		}
		snapshot.lineage = input.Lineage
	}
	if _, err = intakeAuthorityTx(ctx, tx, team, request); err != nil {
		return AdmissionResult{}, err
	}
	var number int64
	if err = tx.QueryRowContext(ctx, `SELECT next_run_number FROM teams WHERE name=?`, team).Scan(&number); err != nil {
		return AdmissionResult{}, err
	}
	result := AdmissionResult{RunID: id, Number: number, Request: snapshot.request}
	if request.IdempotencyKey == "" {
		scope = ""
		digest = ""
	}
	if err = enforceRunsPerHourTx(ctx, tx, team, id, principal.Capability.Subject, now); err != nil {
		var refused *ComputeLimitError
		if errors.As(err, &refused) {
			err = errors.Join(err, &apierr.Error{Code: apierr.RateLimited, Message: "hourly run admission limit reached", Details: map[string]any{"limit": refused.Limit, "cap": refused.Cap, "observed": refused.Observed, "retry_after_ms": 5000}})
		}
		return AdmissionResult{}, err
	}
	if err = insertAdmissionTx(ctx, tx, team, result, snapshot, principal, request.IdempotencyKey, scope, string(digest), policy, now); err != nil {
		return AdmissionResult{}, err
	}
	if err := bindSourceUploadTx(ctx, tx, team, request, result, principal); err != nil {
		return AdmissionResult{}, err
	}
	input := snapshot.request.Request
	if input.Mode == runspec.IntakeExecute {
		result.OwnerUnit = &OwnerLeaseRef{Kind: OwnerTrigger, RunID: id}
		if input.Options.Runner.Set && input.Options.Runner.Value.Kind == runspec.RunnerHere {
			issued, err := tx.store.Issue(ctx, tx, capability.IssueRequest{Kind: capability.Award, Award: &capability.AwardSpec{Claimant: request.Authority, Unit: capability.UnitTrigger, UnitID: id, Purpose: capability.PurposeHere, Delivery: capability.DeliveryDirect, HereRunID: id}})
			if err != nil {
				return AdmissionResult{}, err
			}
			result.HereCapabilityPrefix, result.HereBearer = issued.Capability.Prefix, []byte(issued.Raw)
		}
	}
	if snapshot.spec != nil {
		if err = writeAcceptedPlanTx(ctx, tx, team, id, *snapshot.spec, snapshot.plan, 0); err != nil {
			return AdmissionResult{}, err
		}
	}
	if _, err = intakeAuthorityTx(ctx, tx, team, request); err != nil {
		return AdmissionResult{}, err
	}
	counter, err := tx.ExecContext(ctx, `UPDATE teams SET next_run_number=next_run_number+1 WHERE name=? AND next_run_number=?`, team, number)
	if err != nil {
		return AdmissionResult{}, err
	}
	changed, err := counter.RowsAffected()
	if err != nil {
		return AdmissionResult{}, err
	}
	if changed != 1 {
		return AdmissionResult{}, authorityError(apierr.Conflict, "run number allocation changed")
	}
	if err = writeRunEffectsTx(ctx, tx, team, id, RunPending, now); err != nil {
		return AdmissionResult{}, err
	}
	return result, nil
}

func intakeAuthorityTx(ctx context.Context, tx *storeTx, team Team, request IntakeRequest) (capability.Principal, error) {
	p, err := tx.ResolveAuthority(ctx, request.Authority)
	if err != nil {
		return p, err
	}
	c := p.Capability
	if c.Team != string(team) || c.Audience != capability.Controller || request.Request.Team != "" && request.Request.Team != string(team) || request.CapabilityPrefix != "" && request.CapabilityPrefix != c.Prefix {
		return p, authorityError(apierr.Forbidden, "intake authority does not match the team")
	}
	grant := capability.RunsWrite
	if request.Request.Lineage.RetryOf != "" {
		grant = capability.RunsControl
	}
	if request.Request.Lineage.ParentRunID != "" {
		grant = capability.ChildrenStart
	}
	if !slices.Contains(c.Grants, grant) {
		return p, authorityError(apierr.Forbidden, "intake authority lacks the required grant")
	}
	if grant == capability.ChildrenStart {
		if c.Binding.Kind != capability.BindingNode || c.Binding.RunID != request.Request.Lineage.ParentRunID || c.Binding.NodeID != request.Request.Lineage.ParentJobID || request.Request.Lineage.RetryOf != "" {
			return p, holderRefusal()
		}
		if err = lockStateRunTx(ctx, tx, team, c.Binding.RunID); err != nil {
			return p, err
		}
		row, err := readNodeTx(ctx, tx, team, NodeRef{c.Binding.RunID, c.Binding.NodeID})
		if err != nil {
			return p, err
		}
		actor := Actor{Kind: ActorHolder, Authority: request.Authority, CapabilityPrefix: c.Prefix, Holder: c.Binding.Holder, Generation: c.Binding.Generation}
		if row.Status == StatusDone || row.CancelRequestedAt != nil {
			return p, holderRefusal()
		}
		if err = checkHolderTx(ctx, tx, team, row, actor, tx.now()); err != nil {
			return p, err
		}
		var cancelled sql.NullInt64
		if err = tx.QueryRowContext(ctx, `SELECT cancel_requested_at FROM runs WHERE team=? AND id=?`, team, c.Binding.RunID).Scan(&cancelled); err != nil {
			return p, err
		}
		if cancelled.Valid {
			return p, holderRefusal()
		}
	} else if c.Binding.Kind != capability.BindingNone && c.Binding.Kind != capability.BindingPush {
		return p, holderRefusal()
	}
	if c.Binding.Kind == capability.BindingPush {
		push := c.Binding.Push
		git := request.Request.Git
		if push == nil || string(git.Repository.Key) != push.RepositoryKey || git.Ref != push.Ref || git.SHA != push.SHA || request.Request.Lineage.RetryOf != "" {
			return p, holderRefusal()
		}
	}
	return p, nil
}

func retryIntakeTx(ctx context.Context, tx *storeTx, team Team, request IntakeRequest, p capability.Principal) (admittedSnapshot, error) {
	if request.Request.Lineage.RetryOf == "" {
		if request.RetryMode != "" {
			return admittedSnapshot{}, authorityError(apierr.BadRequest, "retry mode requires a source run")
		}
		return admittedSnapshot{}, nil
	}
	mode := request.RetryMode
	if mode == "" {
		mode = "failed"
	}
	if mode != "failed" && mode != "all" {
		return admittedSnapshot{}, authorityError(apierr.BadRequest, "unknown retry mode")
	}
	if err := lockStateRunTx(ctx, tx, team, request.Request.Lineage.RetryOf); err != nil {
		return admittedSnapshot{}, err
	}
	source, err := readRunTx(ctx, tx, team, request.Request.Lineage.RetryOf)
	if err != nil {
		return admittedSnapshot{}, err
	}
	if request.Request.Pipeline != "" && request.Request.Pipeline != source.Pipeline {
		return admittedSnapshot{}, authorityError(apierr.BadRequest, "retry pipeline differs from its source")
	}
	if source.Request.Request.Mode != runspec.IntakeExecute {
		return admittedSnapshot{}, authorityError(apierr.BadRequest, "published history cannot be retried")
	}
	in := runspec.RetryInput{Request: source.Request, Plan: source.Plan, Retrier: runspec.Actor{Kind: string(p.Capability.Subject.Kind), ID: p.Capability.Subject.ID}, SourceRunID: source.ID}
	if source.Spec != nil {
		in.Spec = runspec.Optional[runspec.Spec]{Set: true, Value: *source.Spec}
	}
	carried, err := tx.store.intakeServices().CarryRetry(in)
	if err != nil {
		return admittedSnapshot{}, err
	}
	out := admittedSnapshot{request: carried.Request, plan: carried.Plan, lineage: carried.Lineage, retry: &RetrySelection{Mode: mode}}
	if carried.Spec.Set {
		out.spec = &carried.Spec.Value
	}
	return out, nil
}

func trustedIntakeRequest(request IntakeRequest, p capability.Principal, team Team, policy IntakePolicyFacts) (runspec.Request, error) {
	input := request.Request
	if input.Burst != nil {
		if p.Capability.Subject.Kind != capability.SubjectAccount {
			return input, bindingRefusal()
		}
		if err := input.Burst.Validate(); err != nil {
			return input, authorityError(apierr.BadRequest, err.Error())
		}
	}
	if input.Pipeline == "" || input.Mode != runspec.IntakeExecute && input.Mode != runspec.IntakePublish || input.Dispatch != runspec.DispatchHost && input.Dispatch != runspec.DispatchController {
		return input, authorityError(apierr.BadRequest, "invalid intake mode, dispatch or pipeline")
	}
	input.Team = string(team)
	if policy.Options == nil {
		return input, authorityError(apierr.Unavailable, "intake settings resolution is unavailable")
	}
	input.Options = *policy.Options
	input.Actor = runspec.Actor{Kind: string(p.Capability.Subject.Kind), ID: p.Capability.Subject.ID}
	input.Git.App = runspec.Optional[runspec.AppFacts]{}
	input.Git.DefaultBranch = runspec.Optional[string]{}
	if request.VerifiedDeliveryID != "" {
		delivery := policy.Delivery
		if delivery == nil || !delivery.Git.App.Set || delivery.Git.App.Value.DeliveryKey == "" || delivery.Git.App.Value.VerifiedAt.IsZero() {
			return input, authorityError(apierr.Unauthenticated, "verified App delivery is unavailable")
		}
		input.Git, input.Actor, input.PullRequest = delivery.Git, delivery.Actor, delivery.PullRequest
		input.Origin = runspec.OriginApp
	} else if input.Origin == runspec.OriginApp || policy.Delivery != nil {
		return input, authorityError(apierr.Forbidden, "App intake requires a verified delivery")
	}
	if input.PullRequest.Set && (input.PullRequest.Value.Fork || input.PullRequest.Value.HeadRepo != input.PullRequest.Value.BaseRepo) {
		return input, authorityError(apierr.Forbidden, "fork pull requests cannot execute")
	}
	if input.Git.Repository.Key == "" {
		return input, authorityError(apierr.BadRequest, "repository identity is required")
	}
	if policy.SourceLocation != nil {
		input.Source.Location = *policy.SourceLocation
	} else if input.Source.Location != (runspec.SourceLocation{}) {
		return input, authorityError(apierr.Unavailable, "verified source location is unavailable")
	}
	if input.Lineage.ParentRunID != "" {
		input.Origin = runspec.OriginChild
	}
	if input.Mode == runspec.IntakePublish {
		input.Origin = runspec.OriginPublish
	}
	return input, nil
}

func intakePoolsTx(ctx context.Context, tx *storeTx, team Team, input *runspec.Request, authorized bool) ([]string, error) {
	pools := []string{}
	if !authorized {
		return pools, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT pool,branch,source,route FROM pool_access_rules WHERE team=? AND repo_key=? ORDER BY pool,id`+tx.forShare(), team, input.Git.Repository.Key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pool, branch, source, route string
		if err = rows.Scan(&pool, &branch, &source, &route); err != nil {
			return nil, err
		}
		if source != "" && source != string(input.Origin) {
			continue
		}
		if branch != "" && (!input.Git.App.Set || branch != input.Git.App.Value.Ref) {
			continue
		}
		if route != "" {
			requirement := "pool=" + pool
			if !slices.Contains(input.Options.Requires.Value, requirement) {
				input.Options.Requires.Value = append(slices.Clone(input.Options.Requires.Value), requirement)
			}
			if !input.Options.Requires.Set {
				input.Options.Requires.Set = true
				input.Options.Requires.Origin = runspec.SettingOrigin{Layer: runspec.LayerOperatorTeam, Key: "pool_access_rules"}
			}
		}
		if !slices.Contains(pools, pool) {
			pools = append(pools, pool)
		}
	}
	return pools, rows.Err()
}

func replayAdmissionTx(ctx context.Context, tx *storeTx, team Team, scope, key, digest string) (AdmissionResult, bool, error) {
	var id, previous string
	err := tx.QueryRowContext(ctx, `SELECT id,idempotency_digest FROM runs WHERE team=? AND idempotency_scope=? AND idempotency_key=?`, team, scope, key).Scan(&id, &previous)
	if errors.Is(err, sql.ErrNoRows) {
		return AdmissionResult{}, false, nil
	}
	if err != nil {
		return AdmissionResult{}, false, err
	}
	if previous != digest {
		return AdmissionResult{}, true, authorityError(apierr.Conflict, "idempotency key has different intake inputs")
	}
	result, err := admissionResultTx(ctx, tx, team, id)
	return result, true, err
}

func admissionResultTx(ctx context.Context, tx *storeTx, team Team, id string) (AdmissionResult, error) {
	run, err := readRunTx(ctx, tx, team, id)
	if err != nil {
		return AdmissionResult{}, err
	}
	result := AdmissionResult{RunID: id, Number: run.Number, Request: run.Request, OwnerUnit: run.Owner, Replayed: true}
	if run.Request.Request.Mode == runspec.IntakePublish {
		result.OwnerUnit = nil
	}
	if run.Request.Request.Options.Runner.Value.Kind == runspec.RunnerHere {
		err = tx.QueryRowContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND bind_run=? AND award_purpose='here' ORDER BY created_at LIMIT 1`, team, id).Scan(&result.HereCapabilityPrefix)
		if err != nil {
			return AdmissionResult{}, err
		}
	}
	return result, nil
}

func appReplayKey(input runspec.Request) string {
	if !input.Git.App.Set {
		return ""
	}
	return fmt.Sprintf("%s:%s", input.Git.App.Value.DeliveryKey, input.Pipeline)
}

func validateChildLineageTx(ctx context.Context, tx *storeTx, team Team, input runspec.Request) error {
	if input.Lineage.RepoInherited && input.Lineage.ParentRunID == "" {
		return authorityError(apierr.BadRequest, "repository inheritance requires a parent")
	}
	seen := map[string]bool{}
	for id := input.Lineage.ParentRunID; id != ""; {
		if seen[id] {
			return authorityError(apierr.Conflict, "child pipeline cycle")
		}
		seen[id] = true
		ancestor, err := readRunTx(ctx, tx, team, id)
		if err != nil {
			return err
		}
		parent := ancestor.Request.Request
		if id == input.Lineage.ParentRunID && input.Lineage.RepoInherited && parent.Git.Repository.Key != input.Git.Repository.Key {
			return authorityError(apierr.BadRequest, "inherited repository differs from its parent")
		}
		if parent.Pipeline == input.Pipeline && parent.Git.Repository.Key == input.Git.Repository.Key {
			return authorityError(apierr.Conflict, "child pipeline cycle")
		}
		id = ancestor.Lineage.ParentRunID
	}
	return nil
}

func intakeScope(principal capability.Principal, pipeline string) (string, error) {
	subject := principal.Capability.Subject
	if principal.Authority.Kind == "schedule" {
		subject.ID, pipeline = principal.Authority.ID, ""
	}
	return stateHash(struct {
		Subject  capability.Subject
		Pipeline string
	}{subject, pipeline})
}
