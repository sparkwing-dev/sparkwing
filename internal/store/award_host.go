package store

import (
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func awardExecutionHost(chain []authorityNode, sealed runspec.SealedRequest, here bool) string {
	for _, node := range chain {
		c := node.principal.Capability
		if c.Unit != "" {
			return c.ExecutionHost
		}
		if capability.Launcher(c) {
			return ""
		}
		if c.Subject.Kind == capability.SubjectLocal || c.Subject.Kind == capability.SubjectMachine && (c.Binding.Kind == capability.BindingNone || c.Binding.Kind == capability.BindingPush) {
			return c.Subject.ID
		}
	}
	if here {
		return sealed.Request.OriginMachine
	}
	return ""
}

// Only authenticated activation of a reserved execution node seals launcher placement.
// Capacity demand and reservation observations never supply an execution host.
func (s *Store) sealLauncherHost(ctx context.Context, authority capability.AuthorityRef, observation capability.LauncherActivation) error {
	if _, err := decodeAwardUnit(observation.Award.Kind, observation.Award.ID); err != nil {
		return nil
	}
	return s.inLauncherBatchTx(ctx, authority, []capability.LeaseRef{observation.Award}, true, func(tx *storeTx) error {
		chain, err := authorityChainTx(ctx, tx, authority, true)
		if err != nil {
			return err
		}
		claimant, root := chain[0].principal, chain[len(chain)-1].principal
		if !capability.Launcher(claimant.Capability) || observation.NodeUID == "" {
			return bindingRefusal()
		}
		row, err := tx.operator.loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: observation.Award.Kind, UnitID: observation.Award.ID})
		if endedLauncherPreparation(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.prefix == "" || row.holder != observation.Award.Holder || row.generation != observation.Award.Generation {
			return nil
		}
		award, live, err := readLauncherAwardTx(ctx, tx, row.prefix, claimant.Capability, root)
		if err != nil || !live {
			return err
		}
		host := "node:" + observation.NodeUID
		if award.Capability.ExecutionHost != "" {
			if award.Capability.ExecutionHost != host {
				return bindingRefusal()
			}
			return nil
		}
		if award.Capability.Delivery != capability.DeliveryBootstrap {
			return bindingRefusal()
		}
		record, err := tx.operator.loadCapabilityRecordTx(ctx, tx, row.prefix)
		if err != nil {
			return err
		}
		record.Value.ExecutionHost = host
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		_, err = tx.execContext(ctx, `UPDATE capabilities SET authority_json=? WHERE team=? AND prefix=?`, body, award.Capability.Team, row.prefix)
		return err
	})
}
