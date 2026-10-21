package store

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type consolePolicy func(capability.Principal, time.Time) bool

func consoleAuthorityTx(ctx context.Context, tx *storeTx, a capability.Authorized, path string) error {
	route, ok := capability.LookupRoute("POST", path)
	if !ok || a.Principal.Class != capability.CredentialConsole || !reflect.DeepEqual(a.Target, capability.Target{}) || a.ResultReplay || a.LogTailAttempt != 0 {
		return bindingRefusal()
	}
	if tx.store.consolePolicy == nil {
		return authorityError(apierr.Unavailable, "console policy is unavailable")
	}
	p, err := tx.ResolveAuthority(ctx, a.Principal.Authority)
	if err != nil {
		return err
	}
	if !p.DirectAccountSession() || a.Principal.Capability.Subject != (capability.Subject{Kind: capability.SubjectOperator, ID: p.Capability.Subject.ID}) || !tx.store.consolePolicy(p, tx.now()) {
		return bindingRefusal()
	}
	for _, access := range route.Access {
		if access.Credential == capability.CredentialOperator && reflect.DeepEqual(access, a.Access) {
			return nil
		}
	}
	return bindingRefusal()
}

// SetRunnerMetered changes future awards for one enrolled machine after a console recheck.
func (o *Operator) SetRunnerMetered(ctx context.Context, a capability.Authorized, prefix string, metered bool) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		if err := acquireLockSet(ctx, tx, func() lockSet {
			return discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority, {Kind: capability.AuthorityCapability, ID: prefix}}, prefix)
		}); err != nil {
			return err
		}
		path := "/api/v1/operator/runners/{runner}/metered"
		if err := consoleAuthorityTx(ctx, tx, a, path); err != nil {
			return err
		}
		record, err := tx.operator.loadCapabilityRecordTx(ctx, tx, prefix)
		if err != nil {
			return err
		}
		c := &record.Value
		if !registeredClaimant(*c) || c.Parent != nil || c.Unit != "" || record.Origin == nil || record.Origin.Kind != capability.ProvisionMachine || record.Origin.RegistrationID != "runner:"+c.Team+":"+c.Subject.ID {
			return bindingRefusal()
		}
		if _, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}); err != nil {
			return err
		}
		c.RootMetered = metered
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := consoleAuthorityTx(ctx, tx, a, path); err != nil {
			return err
		}
		if _, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}); err != nil {
			return err
		}
		p, err := resolveProvisionAuthorityTx(ctx, tx, a.Principal.Authority, capability.ProofForConsole(a.Principal))
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE capabilities SET metered=?,authority_json=? WHERE prefix=? AND team=?`, boolInt(metered), body, prefix, c.Team); err != nil {
			return err
		}
		return recordMutationAuditTx(tx, Team(c.Team), auditPrincipalActor(p), "runner.metered", prefix)
	})
}
