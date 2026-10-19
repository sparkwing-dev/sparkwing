package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
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
			rows, err := tx.QueryContext(ctx, `SELECT DISTINCT c.prefix,c.subject_id FROM capabilities c JOIN runs r ON r.team=c.team AND r.created_principal=c.created_by WHERE r.team=? AND r.id=? AND r.created_subject_kind='account' AND c.subject_kind='machine' AND c.bind_kind='none' AND c.revoked_at IS NULL AND (c.expires_at IS NULL OR c.expires_at>?)`, team, run, tx.now().UnixNano())
			if err != nil {
				return nil, err
			}
			candidates := map[string]string{}
			for rows.Next() {
				var prefix, name string
				if err := rows.Scan(&prefix, &name); err != nil {
					return nil, errors.Join(err, rows.Close())
				}
				candidates[prefix] = name
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return nil, err
			}
			for prefix, name := range candidates {
				record, err := loadCapabilityRecordTx(ctx, tx, prefix)
				if err != nil {
					return nil, err
				}
				if record.Origin != nil && record.Origin.Personal {
					prefers = append(prefers, "name="+name)
				}
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
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT created_by FROM capabilities WHERE team=? AND prefix=?`, root.Team, root.Prefix).Scan(&owner); err != nil {
		return err
	}
	requester, err := runRequesterAccountTx(ctx, tx, Team(root.Team), run)
	if err != nil {
		return err
	}
	if owner == "" || owner != requester {
		return authorityError(apierr.Forbidden, "personal machine is reserved for its owner's runs")
	}
	return nil
}

func checkBurstClaimantTx(ctx context.Context, tx *storeTx, team Team, chain []authorityNode, unit UnitRef) error {
	burst, err := burstPlacementTx(ctx, tx, team, unit.RunID)
	if err != nil || burst == nil {
		return err
	}
	here, cloud := false, false
	for _, ancestor := range chain {
		a := ancestor.principal.Capability
		here = here || a.Purpose == capability.PurposeHere && a.Binding.RunID == unit.RunID
		cloud = cloud || launcherClaimant(a)
	}
	root := chain[len(chain)-1].principal.Capability
	eligible := here || cloud
	if root.Subject.Kind == capability.SubjectMachine && root.Parent == nil {
		record, err := loadCapabilityRecordTx(ctx, tx, root.Prefix)
		if err != nil {
			return err
		}
		eligible = record.Origin != nil && record.Origin.Personal
		if eligible {
			if err := checkPersonalMachineTx(ctx, tx, chain, unit.RunID); err != nil {
				return err
			}
		}
	}
	if !eligible {
		return authorityError(apierr.Forbidden, "burst work is reserved for the requester and selected Cloud executors")
	}
	if here && unit.Kind != capability.UnitNode {
		return nil
	}
	profile := authenticatedClaimProfile(root, match.Profile{}, here)
	if cloud {
		if !slices.ContainsFunc(burst.Preferences, func(step runspec.PlacementStep) bool { return step.Target == "cloud" }) {
			return authorityError(apierr.Forbidden, "Cloud is outside the sealed burst preference set")
		}
		profile.Class = match.ClassCloud
	}
	preferences, err := burstPreferencesTx(ctx, tx, team, unit.RunID, *burst)
	if err != nil {
		return err
	}
	if placementRank(profile, match.Demand{AllowedPools: profile.ReservedPools}, preferences) == len(preferences) {
		return authorityError(apierr.Forbidden, "claimant is outside the sealed burst preference set")
	}
	return nil
}
