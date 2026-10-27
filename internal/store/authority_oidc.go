package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// OIDCMint is the record of one signed ID token. Token is returned to the
// caller and never stored; ID, Subject and ExpiresAt are audited.
type OIDCMint struct {
	Token     string
	ID        string
	Subject   string
	ExpiresAt time.Time
}

// MintOIDCToken revalidates claim authority and sealed identity, has sign
// produce the token from those facts, and audits it, all in one transaction:
// a token exists only if its audit row commits alongside the authority check
// that allowed it.
func (t *Tenant) MintOIDCToken(ctx context.Context, supplied capability.Authorized, audience string, sign func(capability.OIDCFacts) (OIDCMint, error)) (OIDCMint, error) {
	return inTxResult(ctx, t.s, func(tx *storeTx) (OIDCMint, error) {
		facts, err := t.oidcFactsTx(ctx, tx, supplied)
		if err != nil {
			return OIDCMint{}, err
		}
		minted, err := sign(facts)
		if err != nil {
			return OIDCMint{}, err
		}
		if minted.Token == "" || minted.ID == "" || minted.Subject == "" || minted.ExpiresAt.IsZero() || minted.ExpiresAt.After(facts.NotAfter) {
			return OIDCMint{}, authorityError(apierr.IntegrityFailure, "signed OIDC token does not match its issuance facts")
		}
		event := auditOIDCMint{RunID: supplied.Target.RunID, Node: digestAudit(supplied.Principal.Capability.Binding.NodeID), Audience: audience, TokenID: minted.ID, Subject: minted.Subject, ExpiresAt: minted.ExpiresAt.UTC().Unix()}
		if err := tx.operator.recordAudit(ctx, tx, auditContext{At: tx.now(ctx), Team: t.team, Actor: auditPrincipalActor(supplied.Principal)}, event); err != nil {
			return OIDCMint{}, err
		}
		return minted, nil
	})
}

func (t *Tenant) oidcFactsTx(ctx context.Context, tx *storeTx, supplied capability.Authorized) (capability.OIDCFacts, error) {
	chain, err := teamAuthorityChainTx(ctx, tx, t.team, supplied.Principal.Authority, true, sameTeam)
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	state, err := authenticationBindingTx(ctx, tx, chain[0].principal.Capability)
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	snapshot := capability.AuthenticationSnapshot{Now: tx.now(ctx), Binding: state}
	for _, node := range chain {
		snapshot.Authorities = append(snapshot.Authorities, capability.AuthorityState{Principal: node.principal})
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
	if c := auth.Principal.Capability; len(c.ReservedPools) > 0 {
		serving, err := unitPoolsTx(ctx, tx, t.team, run.ID, c.Unit, c.Binding.NodeID, c.ReservedPools)
		if err != nil {
			return capability.OIDCFacts{}, err
		}
		if len(serving) == 0 {
			return capability.OIDCFacts{}, bindingRefusal()
		}
	}
	factsClass, err := claimantFactsTx(ctx, tx, chain, runspec.Request{})
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	facts, err := oidcIdentity(t.team, run)
	if err != nil {
		return capability.OIDCFacts{}, err
	}
	facts.Now, facts.RunnerKind = snapshot.Now, factsClass.OIDCKind
	facts.NotAfter = state.LeaseExpiresAt
	if state.Expansion != nil {
		facts.NotAfter = state.Expansion.ExpiresAt
	}
	facts.NotAfter = authorityNotAfter(chain, facts.NotAfter)
	if !facts.NotAfter.After(facts.Now) {
		return capability.OIDCFacts{}, authorityError(apierr.Unauthenticated, "claim issuance window has ended")
	}
	return facts, nil
}

// oidcIdentity is the part of an ID token that the run's sealed provenance alone decides.
func oidcIdentity(team Team, run RunRow) (capability.OIDCFacts, error) {
	provenance := run.Request.Provenance
	if !provenance.Valid() {
		return capability.OIDCFacts{}, authorityError(apierr.IntegrityFailure, "run has no sealed provenance")
	}
	facts := capability.OIDCFacts{Team: string(team), RunID: run.ID, Pipeline: provenance.Pipeline()}
	facts.Trigger, facts.RepositoryID, facts.Ref, facts.SHA = provenance.OIDC()
	facts.Repository = provenance.Repository()
	return facts, nil
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

func authorityNotAfter(chain []authorityNode, ceiling time.Time) time.Time {
	for _, node := range chain {
		c := node.principal.Capability
		for _, end := range []*time.Time{c.ExpiresAt, c.RevokedAt, c.Binding.Deadline} {
			if end != nil && (ceiling.IsZero() || end.Before(ceiling)) {
				ceiling = *end
			}
		}
	}
	return ceiling
}
