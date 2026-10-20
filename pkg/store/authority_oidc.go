package store

import (
	"context"
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
	personal, err := personalRootTx(ctx, tx, chain)
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	facts, err := oidcIdentity(t.team, run)
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	facts.Now, facts.RunnerKind = snapshot.Now, oidcRunnerKind(chain, personal)
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
	if err := tx.Commit(); err != nil {
		return capability.OIDCFacts{}, err
	}
	return facts, nil
}

// oidcIdentity is the part of an ID token that the run's sealed provenance alone decides.
func oidcIdentity(team Team, run RunRow) (capability.OIDCFacts, error) {
	provenance := run.Request.Provenance
	if !provenance.Valid() {
		return capability.OIDCFacts{}, authorityError(apierr.IntegrityFailure, "run has no sealed provenance")
	}
	facts := capability.OIDCFacts{Team: string(team), RunID: run.ID, Pipeline: provenance.Pipeline(), Repository: run.Request.Request.Git.Repository.Display}
	facts.Trigger, facts.RepositoryID, facts.Ref, facts.SHA = provenance.OIDC()
	return facts, nil
}

func oidcRunnerKind(chain []authorityNode, personal bool) string {
	for _, node := range chain {
		if node.principal.Capability.Binding.Kind == capability.BindingPush {
			return "github-actions"
		}
	}
	switch chain[0].principal.Capability.Subject.Kind {
	case capability.SubjectMachine:
		if personal {
			return "personal"
		}
		return "runner"
	case capability.SubjectService, capability.SubjectOperator:
		return "service"
	default:
		return "user"
	}
}

func originalRequestTx(ctx context.Context, tx *storeTx, team Team, request runspec.SealedRequest) (runspec.SealedRequest, error) {
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
