package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
			record, err := loadCapabilityRecordTx(ctx, tx, root.Prefix)
			if err != nil || record.Origin == nil || !record.Origin.Personal || kind != string(capability.SubjectAccount) {
				return err
			}
			var enrolledBy string
			if err := tx.QueryRowContext(ctx, `SELECT created_by FROM capabilities WHERE team=? AND prefix=?`, t.team, root.Prefix).Scan(&enrolledBy); err != nil {
				return err
			}
			owner = enrolledBy == requester
		}
		return nil
	})
	return owner, err
}
