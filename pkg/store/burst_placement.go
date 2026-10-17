package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func burstPlacementTx(ctx context.Context, tx *storeTx, team Team, run string) (*runspec.BurstPlacement, error) {
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT request_json FROM runs WHERE team=? AND id=?`, team, run).Scan(&body); err != nil {
		return nil, err
	}
	var seal runspec.SealedRequest
	if err := json.Unmarshal(body, &seal); err != nil {
		return nil, err
	}
	return seal.Request.Burst, nil
}

func burstPreferencesTx(ctx context.Context, tx *storeTx, team Team, run string, b runspec.BurstPlacement) ([]PlacementPreference, error) {
	out := make([]PlacementPreference, 0, len(b.Preferences))
	for _, step := range b.Preferences {
		target := step.Target
		switch target {
		case "here":
			target = "class=coordinator"
		case "machines":
			target = "name="
		case "cloud":
			target = "class=cloud"
		}
		prefers := []string{target}
		if step.Target == "machines" {
			rows, err := tx.QueryContext(ctx, `SELECT DISTINCT c.subject_id FROM capabilities c JOIN runs r ON r.team=c.team AND r.created_principal=c.created_by WHERE r.team=? AND r.id=? AND r.created_subject_kind='account' AND c.subject_kind='machine' AND c.bind_kind='none'`, team, run)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					return nil, errors.Join(err, rows.Close())
				}
				prefers = append(prefers, "name="+name)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return nil, err
			}
		}
		out = append(out, PlacementPreference{Prefers: []string{strings.Join(prefers, ",")}, Hold: step.Hold})
	}
	return out, nil
}

func checkPersonalMachineTx(ctx context.Context, tx *storeTx, chain []authorityNode, run string) error {
	root := chain[len(chain)-1].principal.Capability
	if root.Subject.Kind != capability.SubjectMachine || root.Parent != nil {
		return nil
	}
	record, err := loadCapabilityRecordTx(ctx, tx, root.Prefix)
	if err != nil {
		return err
	}
	if record.Origin == nil || !record.Origin.Personal {
		return nil
	}
	var owner, requester, kind string
	if err := tx.QueryRowContext(ctx, `SELECT created_by FROM capabilities WHERE team=? AND prefix=?`, root.Team, root.Prefix).Scan(&owner); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT created_principal,created_subject_kind FROM runs WHERE team=? AND id=?`, root.Team, run).Scan(&requester, &kind); err != nil {
		return err
	}
	if owner == "" || owner != requester || kind != string(capability.SubjectAccount) {
		return authorityError(apierr.Forbidden, "personal machine is reserved for its owner's runs")
	}
	return nil
}
