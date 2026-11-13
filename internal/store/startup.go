package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type cloudStartupReceipt struct {
	Award   capability.LeaseRef   `json:"award"`
	Unit    UnitRef               `json:"unit"`
	Startup capability.PodStartup `json:"startup"`
}

func (s *Store) recordLauncherStartup(ctx context.Context, authority capability.AuthorityRef, observation capability.LauncherObservation) error {
	return s.inLauncherBatchTx(ctx, authority, []capability.LeaseRef{observation.Award}, true, func(tx *storeTx) error {
		chain, err := authorityChainTx(ctx, tx, authority, true)
		if err != nil {
			return err
		}
		claimant, root := chain[0].principal, chain[len(chain)-1].principal
		if !launcherClaimant(claimant.Capability) {
			return bindingRefusal()
		}
		// safety: fast Pods can finish before the launcher polls; diagnostics require the exact historical award owned by this live launcher.
		var prefix string
		err = tx.QueryRowContext(ctx, `SELECT prefix FROM capabilities WHERE parent_kind=? AND parent_id=? AND subject_kind=? AND subject_id=? AND unit_kind=? AND unit_id=? AND bind_holder=? AND COALESCE(eval_unit_generation,bind_generation)=? AND issuance IN ('award','exchange') ORDER BY created_at LIMIT 1`, root.Authority.Kind, root.Authority.ID, claimant.Capability.Subject.Kind, claimant.Capability.Subject.ID, observation.Award.Kind, observation.Award.ID, observation.Award.Holder, observation.Award.Generation).Scan(&prefix)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		award, err := loadCapabilityTx(ctx, tx, prefix)
		if err != nil {
			return err
		}
		if award.Parent == nil || *award.Parent != root.Authority || award.Subject != claimant.Capability.Subject || award.Unit != observation.Award.Kind || award.UnitID != observation.Award.ID || award.Binding.Holder != observation.Award.Holder {
			return bindingRefusal()
		}
		unit, err := decodeAwardUnit(observation.Award.Kind, observation.Award.ID)
		if err != nil {
			return err
		}
		team := Team(award.Team)
		if err := lockStateRunTx(ctx, tx, team, string(unit.RunID)); err != nil {
			return err
		}
		rows, err := tx.queryContext(ctx, `SELECT payload FROM events WHERE team=? AND run_id=? AND node_id=? AND kind='cloud_startup' ORDER BY seq DESC`, team, unit.RunID, unit.NodeID)
		if err != nil {
			return err
		}
		incoming := *observation.Startup
		var prior *capability.PodStartup
		for rows.Next() {
			var body []byte
			if err := rows.Scan(&body); err != nil {
				_ = rows.Close()
				return err
			}
			var receipt cloudStartupReceipt
			if err := json.Unmarshal(body, &receipt); err != nil {
				_ = rows.Close()
				return err
			}
			if receipt.Award == observation.Award && receipt.Startup.PodUID == incoming.PodUID {
				prior = &receipt.Startup
				break
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if prior != nil {
			incoming = incoming.Retain(*prior)
			if reflect.DeepEqual(*prior, incoming) {
				return nil
			}
		}
		_, err = appendEventTx(ctx, tx, team, string(unit.RunID), string(unit.NodeID), "cloud_startup", cloudStartupReceipt{Award: observation.Award, Unit: unit, Startup: incoming}, tx.now(ctx), false)
		return err
	})
}
