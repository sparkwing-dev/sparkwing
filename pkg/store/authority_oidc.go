package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// ReadOIDCFacts revalidates claim authority and sealed identity in one store snapshot.
func (t *Tenant) ReadOIDCFacts(ctx context.Context, supplied capability.Authorized) (capability.OIDCFacts, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	defer rollbackOrLog(tx)
	chain, err := authorityChainTx(ctx, tx, supplied.Principal.Authority, false)
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	state, err := authenticationBindingTx(ctx, tx, chain[0].principal.Capability)
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	snapshot := capability.AuthenticationSnapshot{Now: tx.now(), Binding: state}
	for _, node := range chain {
		snapshot.Authorities = append(snapshot.Authorities, capability.AuthorityState{Principal: node.principal, MembershipValid: true})
	}
	route, _ := capability.LookupRoute("POST", "/api/v1/runs/{run}/oidc-token")
	auth, err := capability.AuthenticateSnapshot(snapshot, route, capability.RequestEvidence{Target: supplied.Target})
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	if auth.Principal.Capability.Team != string(t.team) || supplied.Target.Team != string(t.team) {
		return capability.OIDCFacts{}, bindingRefusal()
	}
	authorized, err := capability.Authorize(ctx, tx, route, auth, supplied.Target)
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	run, err := readRunTx(ctx, tx, t.team, authorized.Target.RunID)
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	facts := capability.OIDCFacts{Now: snapshot.Now, Team: string(t.team), RunID: run.ID, Pipeline: run.Request.Request.Pipeline, Ref: run.Request.Request.Git.Ref, SHA: run.Request.Request.Git.SHA, Repository: run.Request.Request.Git.Repository.Display, Trigger: "manual", RunnerKind: oidcRunnerKind(chain)}
	facts.NotAfter = state.LeaseExpiresAt
	if state.Expansion != nil {
		facts.NotAfter = state.Expansion.ExpiresAt
		if end := state.Expansion.ControllerLeaseExpiresAt; end != nil && end.Before(facts.NotAfter) {
			facts.NotAfter = *end
		}
	}
	for _, end := range []*time.Time{authorized.Principal.Capability.ExpiresAt, authorized.Binding.Deadline} {
		if end != nil && (facts.NotAfter.IsZero() || end.Before(facts.NotAfter)) {
			facts.NotAfter = *end
		}
	}
	if !facts.NotAfter.After(facts.Now) {
		return capability.OIDCFacts{}, authorityError(apierr.Unauthenticated, "claim issuance window has ended")
	}
	identity, err := oidcOriginalIdentityTx(ctx, tx, t.team, run.Request)
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	if err := oidcTriggerTx(ctx, tx, t.team, identity, &facts); err != nil {
		return capability.OIDCFacts{}, err
	}
	if err := tx.Commit(); err != nil {
		return capability.OIDCFacts{}, err
	}
	return facts, nil
}

func oidcRunnerKind(chain []authorityNode) string {
	for _, node := range chain {
		if node.principal.Capability.Binding.Kind == capability.BindingPush {
			return "github-actions"
		}
	}
	switch chain[0].principal.Capability.Subject.Kind {
	case capability.SubjectMachine:
		return "runner"
	case capability.SubjectService, capability.SubjectOperator:
		return "service"
	default:
		return "user"
	}
}

func oidcOriginalIdentityTx(ctx context.Context, tx *storeTx, team Team, request runspec.SealedRequest) (runspec.SealedRequest, error) {
	seen := map[string]bool{request.RunID: true}
	for {
		origin, lineage := request.Request.Origin, request.Request.Lineage
		parent := ""
		if origin == runspec.OriginRetry {
			parent = lineage.RetryOf
		} else if origin == runspec.OriginChild {
			parent = lineage.ParentRunID
		}
		if parent == "" {
			return request, nil
		}
		if seen[parent] {
			return runspec.SealedRequest{}, authorityError(apierr.IntegrityFailure, "cyclic run identity lineage")
		}
		seen[parent] = true
		run, err := readRunTx(ctx, tx, team, parent)
		if err != nil {
			return runspec.SealedRequest{}, err
		}
		request = run.Request
	}
}

func oidcTriggerTx(ctx context.Context, tx *storeTx, team Team, identity runspec.SealedRequest, facts *capability.OIDCFacts) error {
	request := identity.Request
	if request.Origin == runspec.OriginSchedule {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cron_fires WHERE team=? AND run_id=? AND outcome='fired'`, team, identity.RunID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			facts.Trigger = "cron"
		}
	}
	app := request.Git.App
	if request.Origin != runspec.OriginApp || !app.Set || app.Value.VerifiedAt.IsZero() || app.Value.RepositoryID <= 0 || app.Value.DeliveryKey == "" {
		return nil
	}
	var event string
	err := tx.QueryRowContext(ctx, `SELECT event FROM github_app_deliveries WHERE digest=?`, app.Value.DeliveryKey).Scan(&event)
	if errors.Is(err, sql.ErrNoRows) {
		return authorityError(apierr.Unavailable, "verified run event is unavailable")
	}
	if err != nil {
		return err
	}
	if !slices.Contains([]string{"push", "pull_request", "release", "create", "delete"}, event) {
		return nil
	}
	facts.Trigger, facts.RepositoryID = event, app.Value.RepositoryID
	if event == "pull_request" {
		if !request.PullRequest.Set || request.PullRequest.Value.Number <= 0 {
			return authorityError(apierr.IntegrityFailure, "verified pull request identity is incomplete")
		}
		facts.Ref = fmt.Sprintf("refs/pull/%d/head", request.PullRequest.Value.Number)
	}
	return nil
}
