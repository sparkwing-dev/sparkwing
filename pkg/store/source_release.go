package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
// Repository, host, executor and consent come from persisted authority and the sealed request.
// The controller decrypts the returned credential; neither a host nor executor facts are client inputs.
func (t *Tenant) ReleaseSourceCredential(ctx context.Context, authorized capability.Authorized) (GitCredential, capability.SourceReleaseAudit, error) {
	var credential GitCredential
	var audit capability.SourceReleaseAudit
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		request, err := t.sourceReleaseRequestTx(ctx, tx, authorized)
		if err != nil {
			return err
		}
		credential, err = scanGitCredential(tx.QueryRowContext(ctx, `SELECT `+gitCredentialCols+` FROM git_credentials WHERE team=? AND host=?`+tx.forUpdate(), t.team, request.Host))
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
		if credential.ConfirmedAt == nil || credential.Kind != GitCredentialSSH && credential.Kind != GitCredentialHTTPS {
			return bindingRefusal()
		}
		policy := t.s.sourcePolicy
		if policy == nil {
			policy = sharedSourceReleaseContracts{}
		}
		audit, err = policy.AuthorizeSourceRelease(request, tx.now())
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
		return insertSourceReleaseAuditTx(ctx, tx, audit)
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
	unit := UnitRef{Kind: c.Unit, RunID: b.RunID, NodeID: b.NodeID, SourceGeneration: b.Generation}
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
	if deadline.Valid && deadline.Int64 <= tx.now().UnixNano() {
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
	cloud := p.Capability.Billing != nil && p.Capability.Billing.ExecutorClass == "cloud" && profile.Location == "cloud"
	executor := capability.SourceExecutor{Team: string(t.team), MachineID: root.Subject.ID, Cloud: cloud, AuthorizedRepositories: []string{repo}}
	if cloud {
		return executor, nil
	}
	if root.Subject.Kind != capability.SubjectMachine || root.Team != string(t.team) {
		return capability.SourceExecutor{}, bindingRefusal()
	}
	if err := tx.QueryRowContext(ctx, `SELECT created_by FROM capabilities WHERE team=? AND prefix=?`, t.team, root.Prefix).Scan(&executor.OwnerAccountID); err != nil {
		return capability.SourceExecutor{}, err
	}
	var consent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM git_credential_machines WHERE team=? AND capability_prefix=? AND enabled_by=?`, t.team, root.Prefix, executor.OwnerAccountID).Scan(&consent); err != nil {
		return capability.SourceExecutor{}, err
	}
	executor.OwnerOptedIn = executor.OwnerAccountID != "" && consent == 1
	if !executor.OwnerOptedIn {
		return capability.SourceExecutor{}, bindingRefusal()
	}
	return executor, nil
}

// SetSourceMachineConsent changes only the authenticated account owner's machine consent.
// Team administration cannot substitute for ownership, including when disabling consent.
func (t *Tenant) SetSourceMachineConsent(ctx context.Context, authority capability.AuthorityRef, machine string, enabled bool) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		p, err := tx.ResolveAuthority(ctx, authority)
		if err != nil {
			return err
		}
		if !p.DirectAccountSession() || p.Capability.Team != string(t.team) || p.Capability.Audience != capability.Controller {
			return bindingRefusal()
		}
		root, err := readAuthorityTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: machine}, true)
		if err != nil {
			return err
		}
		c := root.principal.Capability
		if c.Team != string(t.team) || c.Subject.Kind != capability.SubjectMachine || c.Parent != nil || c.Binding.Kind != capability.BindingNone {
			return bindingRefusal()
		}
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT created_by FROM capabilities WHERE team=? AND prefix=?`, t.team, machine).Scan(&owner); err != nil {
			return err
		}
		if owner != p.Capability.Subject.ID {
			return bindingRefusal()
		}
		if _, err := authorityChainTx(ctx, tx, authority, false); err != nil {
			return err
		}
		if !enabled {
			_, err = tx.ExecContext(ctx, `DELETE FROM git_credential_machines WHERE team=? AND capability_prefix=?`, t.team, machine)
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO git_credential_machines(team,capability_prefix,enabled_by,enabled_at) VALUES (?,?,?,?) ON CONFLICT(team,capability_prefix) DO UPDATE SET enabled_by=excluded.enabled_by,enabled_at=excluded.enabled_at`, t.team, machine, owner, tx.now().UnixNano())
		return err
	})
}

func insertSourceReleaseAuditTx(ctx context.Context, tx *storeTx, audit capability.SourceReleaseAudit) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO git_credential_releases(id,team,credential_id,host,repository_key,run_id,node_id,generation,machine_id,owner_account_id,runner,capability_prefix,released_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, audit.ID, audit.Team, audit.CredentialID, audit.Host, audit.RepositoryKey, audit.RunID, audit.NodeID, audit.Generation, audit.MachineID, audit.OwnerAccountID, audit.MachineID, audit.CapabilityPrefix, audit.ReleasedAt.UnixNano())
	return err
}
