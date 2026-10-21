package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type sourceReleaseContracts interface {
	AuthorizeSourceRelease(capability.SourceReleaseRequest, time.Time) (capability.SourceReleaseAudit, error)
}

type sharedSourceReleaseContracts struct{}

func (sharedSourceReleaseContracts) AuthorizeSourceRelease(request capability.SourceReleaseRequest, now time.Time) (capability.SourceReleaseAudit, error) {
	return capability.AuthorizeSourceRelease(request, now)
}

// ReleaseSourceCredential returns the sealed credential only after its authorization audit commits.
// Repository, host and executor come from persisted authority and the sealed request.
// The controller decrypts the returned credential; neither a host nor executor facts are client inputs.
func (t *Tenant) ReleaseSourceCredential(ctx context.Context, authorized capability.Authorized) (GitCredential, capability.SourceReleaseAudit, error) {
	var credential GitCredential
	var audit capability.SourceReleaseAudit
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		request, err := t.sourceReleaseRequestTx(ctx, tx, authorized)
		if err != nil {
			return err
		}
		credential, err = scanGitCredential(tx.QueryRowContext(ctx, `SELECT `+gitCredentialCols+` FROM git_credentials WHERE team=? AND host=? AND (repository_key=? OR host_wide=1) ORDER BY host_wide LIMIT 1`+tx.forUpdate(), t.team, request.Host, request.RepositoryKey))
		if errors.Is(err, sql.ErrNoRows) {
			return errors.Join(ErrNotFound, &apierr.Error{Code: apierr.NotFound, Message: "store a git credential for " + request.Host + " in the team's Git credentials"})
		}
		if err != nil {
			return err
		}
		chain, err := authorityChainTx(ctx, tx, authorized.Principal.Authority, true)
		if err != nil {
			return err
		}
		run, err := readRunTx(ctx, tx, t.team, request.Authorization.Binding.RunID)
		if err != nil {
			return err
		}
		request.Executor, err = t.sourceExecutorTx(ctx, tx, chain, run)
		if err != nil {
			return err
		}
		request.CredentialID = credential.ID
		request.SSH = credential.Kind == GitCredentialSSH
		request.SSHHostConfirmed = credential.ConfirmedAt != nil
		if credential.ConfirmedAt == nil {
			return apierr.Authorization(apierr.ReasonCredentialUnconfirmed, "git credential host must be confirmed")
		}
		if credential.Kind != GitCredentialSSH && credential.Kind != GitCredentialHTTPS {
			return bindingRefusal()
		}
		policy := t.s.sourcePolicy
		if policy == nil {
			policy = sharedSourceReleaseContracts{}
		}
		audit, err = policy.AuthorizeSourceRelease(request, tx.now(ctx))
		if err != nil {
			return err
		}
		// safety: enrollment and policy work may outlast the authority or lease.
		if _, err := authorityChainTx(ctx, tx, authorized.Principal.Authority, false); err != nil {
			return err
		}
		if err := checkSourceReleaseFenceTx(ctx, tx, request.Authorization.Principal.Capability); err != nil {
			return err
		}
		audit.ID, err = newIdentityID()
		if err != nil {
			return err
		}
		return insertSourceReleaseAuditTx(ctx, tx, audit, auditPrincipalActor(chain[0].principal))
	})
	if err != nil {
		return GitCredential{}, capability.SourceReleaseAudit{}, err
	}
	return credential, audit, nil
}

func (t *Tenant) sourceReleaseRequestTx(ctx context.Context, tx *storeTx, authorized capability.Authorized) (capability.SourceReleaseRequest, error) {
	if err := lockClaimControllerTx(ctx, tx); err != nil {
		return capability.SourceReleaseRequest{}, err
	}
	chain, err := authorityChainTx(ctx, tx, authorized.Principal.Authority, true)
	if err != nil {
		return capability.SourceReleaseRequest{}, err
	}
	p := chain[0].principal
	c, b := p.Capability, p.Capability.Binding
	if c.Team != string(t.team) || c.Audience != capability.Controller || c.Unit == "" || b.RunID == "" || authorized.Target.Team != "" && authorized.Target.Team != c.Team || authorized.Target.RunID != b.RunID || authorized.Target.NodeID != "" && authorized.Target.NodeID != b.NodeID {
		return capability.SourceReleaseRequest{}, bindingRefusal()
	}
	unit := UnitRef{Kind: c.Unit, RunID: runspec.RunID(b.RunID), NodeID: runspec.NodeID(b.NodeID), SourceGeneration: b.Generation}
	if b.Expansion != nil {
		unit.Kind = b.Expansion.Kind
	}
	if err := lockClaimOwnerTx(ctx, tx, t.team, unit); err != nil {
		return capability.SourceReleaseRequest{}, err
	}
	if err := lockStateRunTx(ctx, tx, t.team, b.RunID); err != nil {
		return capability.SourceReleaseRequest{}, err
	}
	if b.Kind == capability.BindingNode {
		var id string
		if err := tx.QueryRowContext(ctx, `SELECT node_id FROM nodes WHERE team=? AND run_id=? AND node_id=?`+tx.forUpdate(), t.team, b.RunID, b.NodeID).Scan(&id); err != nil {
			return capability.SourceReleaseRequest{}, err
		}
	}
	if err := checkSourceReleaseFenceTx(ctx, tx, c); err != nil {
		return capability.SourceReleaseRequest{}, err
	}
	run, err := readRunTx(ctx, tx, t.team, b.RunID)
	if err != nil {
		return capability.SourceReleaseRequest{}, err
	}
	host, err := sourceurl.Host(run.Request.Request.Source.Location.RemoteURL)
	if err != nil {
		return capability.SourceReleaseRequest{}, authorityError(apierr.Forbidden, "sealed source has no credential host")
	}
	root := chain[len(chain)-1].principal.Capability
	executor := capability.SourceExecutor{Team: string(t.team), MachineID: root.Subject.ID, AuthorizedRepositories: []string{string(run.Request.Request.Git.Repository.Key)}}
	authorized = capability.Authorized{Principal: p, Binding: b, Target: capability.Target{Team: c.Team, RunID: b.RunID, NodeID: b.NodeID}, Access: capability.AccessRule{Grants: []capability.Grant{capability.SourceRead}, Binding: capability.BindSource, Liveness: capability.LiveSensitive}}
	return capability.SourceReleaseRequest{Authorization: authorized, Executor: executor, RepositoryKey: string(run.Request.Request.Git.Repository.Key), Host: host}, nil
}

func checkSourceReleaseFenceTx(ctx context.Context, tx *storeTx, c capability.Capability) error {
	if err := bindingSourceTx(ctx, tx, c, capability.Target{RunID: c.Binding.RunID}); err != nil {
		return err
	}
	var deadline sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM triggers WHERE team=? AND id=?`, c.Team, c.Binding.RunID).Scan(&deadline); err != nil {
		return err
	}
	now := tx.now(ctx)
	if deadline.Valid && deadline.Int64 <= now.UnixNano() {
		return bindingRefusal()
	}
	return nil
}

func (t *Tenant) sourceExecutorTx(ctx context.Context, tx *storeTx, chain []authorityNode, run RunRow) (capability.SourceExecutor, error) {
	if t.s.claimProfiles == nil {
		return capability.SourceExecutor{}, authorityError(apierr.Unavailable, "source executor enrollment is unavailable")
	}
	p := chain[0].principal
	profile, _, err := t.s.claimProfiles.ResolveClaimProfile(ctx, p)
	if err != nil {
		return capability.SourceExecutor{}, err
	}
	repo := string(run.Request.Request.Git.Repository.Key)
	if !slices.Contains(p.Capability.Repositories, repo) {
		return capability.SourceExecutor{}, bindingRefusal()
	}
	demand := match.Demand{Trigger: true, Repo: &match.Repository{URL: run.Request.Request.Source.Location.RemoteURL}}
	if !match.Evaluate(match.Profile{Accept: profile.Accept}, demand).OK() {
		return capability.SourceExecutor{}, bindingRefusal()
	}
	root := chain[len(chain)-1].principal.Capability
	facts, err := claimantFactsTx(ctx, tx, chain, run.Request.Request)
	if err != nil {
		return capability.SourceExecutor{}, err
	}
	executor := capability.SourceExecutor{Team: string(t.team), MachineID: root.Subject.ID, Cloud: facts.Cloud, Shared: facts.Shared, OwnerAccountID: facts.OwnerAccount, AuthorizedRepositories: []string{repo}}
	if facts.OIDCKind == TierActions {
		return capability.SourceExecutor{}, apierr.Authorization(apierr.ReasonRepositoryUnauthorized, "Actions runners use their own repository credential")
	}
	if facts.Cloud {
		return executor, nil
	}
	if root.Subject.Kind != capability.SubjectMachine || root.Team != string(t.team) {
		return capability.SourceExecutor{}, bindingRefusal()
	}
	if !facts.Shared {
		return capability.SourceExecutor{}, apierr.Authorization(apierr.ReasonPersonalMachine, "team git credentials are released only to shared runners and Cloud work, never to a personal machine")
	}
	return executor, nil
}

func insertSourceReleaseAuditTx(ctx context.Context, tx *storeTx, audit capability.SourceReleaseAudit, actor auditActor) error {
	return tx.operator.recordAudit(ctx, tx, auditContext{At: audit.ReleasedAt, Team: Team(audit.Team), Actor: actor}, auditSourceRelease{AccountID: audit.OwnerAccountID, RunID: audit.RunID, CredentialID: digestAudit(audit.CredentialID), Repository: digestAudit(audit.RepositoryKey), NodeID: digestAudit(audit.NodeID), Generation: audit.Generation, MachineID: digestAudit(audit.MachineID), CapabilityPrefix: digestAudit(audit.CapabilityPrefix)})
}
