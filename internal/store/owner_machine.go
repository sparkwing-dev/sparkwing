package store

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// OwnerMachineClaimant reports whether an award's root credential is the run requester itself or a personal machine the requester enrolled.
func (t *Tenant) OwnerMachineClaimant(ctx context.Context, award capability.Principal, run string) (bool, error) {
	var owner bool
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		chain, err := authorityChainTx(ctx, tx, award.Authority, false)
		if err != nil || len(chain) == 0 {
			return err
		}
		root := chain[len(chain)-1].principal.Capability
		if root.Parent != nil || root.Team != string(t.team) {
			return nil
		}
		var requester, kind string
		if err := tx.QueryRowContext(ctx, `SELECT created_principal,created_subject_kind FROM runs WHERE team=? AND id=?`, t.team, run).Scan(&requester, &kind); err != nil {
			return err
		}
		if requester == "" {
			return nil
		}
		switch root.Subject.Kind {
		case capability.SubjectAccount, capability.SubjectLocal:
			owner = kind == string(root.Subject.Kind) && root.Subject.ID == requester
		case capability.SubjectMachine:
			account, err := runRequesterAccountTx(ctx, tx, t.team, run)
			if err != nil || account == "" {
				return err
			}
			facts, err := claimantFactsTx(ctx, tx, chain, runspec.Request{})
			if err != nil {
				return err
			}
			owner = facts.Personal && facts.OwnerAccount != "" && facts.OwnerAccount == account
			return nil
		}
		return nil
	})
	return owner, err
}

func runRequesterAccountTx(ctx context.Context, tx *storeTx, team Team, run string) (string, error) {
	var requester, kind string
	if err := tx.QueryRowContext(ctx, `SELECT created_principal,created_subject_kind FROM runs WHERE team=? AND id=?`, team, run).Scan(&requester, &kind); err != nil {
		return "", err
	}
	switch kind {
	case string(capability.SubjectAccount):
		return requester, nil
	case string(capability.SubjectMachine):
		return personalMachineOwnerTx(ctx, tx, team, requester)
	}
	return "", nil
}

func personalMachineOwnerTx(ctx context.Context, tx *storeTx, team Team, machine string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT prefix,created_by FROM capabilities WHERE team=? AND subject_kind='machine' AND subject_id=? AND bind_kind='none' AND parent_id IS NULL`, team, machine)
	if err != nil {
		return "", err
	}
	enrolled := map[string]string{}
	for rows.Next() {
		var prefix, by string
		if err := rows.Scan(&prefix, &by); err != nil {
			return "", errors.Join(err, rows.Close())
		}
		enrolled[prefix] = by
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return "", err
	}
	var owner string
	for prefix, by := range enrolled {
		c, err := loadCapabilityTx(ctx, tx, prefix)
		if err != nil {
			return "", err
		}
		facts, err := claimantFactsTx(ctx, tx, []authorityNode{{principal: capability.Principal{Capability: c}}}, runspec.Request{})
		if err != nil {
			return "", err
		}
		if !facts.Personal || by == "" || owner != "" && owner != by {
			return "", nil
		}
		owner = by
	}
	return owner, nil
}
