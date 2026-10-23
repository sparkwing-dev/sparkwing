package store

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func burstPlacementTx(ctx context.Context, tx *storeTx, team Team, run string) (*runspec.BurstPlacement, error) {
	seal, err := sealedRequestTx(ctx, tx, team, run)
	return seal.Request.Burst, err
}

func burstPreferencesTx(ctx context.Context, tx *storeTx, team Team, run string, b runspec.BurstPlacement) ([]PlacementPreference, error) {
	out := make([]PlacementPreference, 0, len(b.Preferences))
	for _, step := range b.Preferences {
		target := step.Target
		switch target {
		case TierHere:
			target = "class=coordinator"
		case "machines":
			target = "name="
		case TierCloud:
			target = "class=cloud"
		}
		prefers := []string{target}
		if step.Target == "machines" {
			names, err := requesterMachineNamesTx(ctx, tx, team, run)
			if err != nil {
				return nil, err
			}
			for _, name := range names {
				prefers = append(prefers, "name="+name)
			}
		}
		out = append(out, PlacementPreference{Prefers: []string{strings.Join(prefers, ",")}, Hold: step.Hold})
	}
	return out, nil
}

func requesterMachineNamesTx(ctx context.Context, tx *storeTx, team Team, run string) ([]string, error) {
	account, err := runRequesterAccountTx(ctx, tx, team, run)
	if err != nil || account == "" {
		return nil, err
	}
	now := tx.now(ctx)
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT prefix,subject_id FROM capabilities WHERE team=? AND created_by=? AND subject_kind='machine' AND bind_kind='none' AND `+liveCredentialSQL("")+``, team, account, now.UnixNano(), now.UnixNano())
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
	var names []string
	for prefix, name := range candidates {
		c, err := loadCapabilityTx(ctx, tx, prefix)
		if err != nil {
			return nil, err
		}
		facts, err := claimantFactsTx(ctx, tx, []authorityNode{{principal: capability.Principal{Capability: c}}}, runspec.Request{})
		if err != nil {
			return nil, err
		}
		if facts.Personal {
			names = append(names, name)
		}
	}
	return names, nil
}

func checkPersonalMachineTx(ctx context.Context, tx *storeTx, chain []authorityNode, run string) error {
	root := chain[len(chain)-1].principal.Capability
	facts, err := claimantFactsTx(ctx, tx, chain, runspec.Request{})
	if err != nil || !facts.Personal {
		return err
	}
	owner := facts.OwnerAccount
	if automated, err := automatedLineageTx(ctx, tx, Team(root.Team), run); err != nil || automated {
		return errors.Join(err, authorityError(apierr.Forbidden, "personal machines take only their owner's manual runs"))
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

// safety: an App or schedule run, or a retry or child of one, never reaches a personal machine.
func automatedLineageTx(ctx context.Context, tx *storeTx, team Team, run string) (bool, error) {
	seal, err := sealedRequestTx(ctx, tx, team, run)
	if err != nil {
		return false, err
	}
	seal.RunID = run
	original, err := originalRequestTx(ctx, tx, team, seal)
	origin := original.Request.Origin
	return origin == runspec.OriginApp || origin == runspec.OriginSchedule, err
}

func checkBurstClaimantTx(ctx context.Context, tx *storeTx, team Team, chain []authorityNode, unit UnitRef) error {
	seal, err := sealedRequestTx(ctx, tx, team, string(unit.RunID))
	burst := seal.Request.Burst
	if err != nil || burst == nil {
		return err
	}
	facts, err := claimantFactsTx(ctx, tx, chain, seal.Request)
	if err != nil {
		return err
	}
	here := false
	for _, ancestor := range chain {
		a := ancestor.principal.Capability
		here = here || a.Purpose == capability.PurposeHere && a.Binding.RunID == string(unit.RunID)
	}
	root := chain[len(chain)-1].principal.Capability
	cloud, eligible := facts.Cloud, here || facts.Cloud || facts.Personal
	if facts.Personal {
		if err := checkPersonalMachineTx(ctx, tx, chain, string(unit.RunID)); err != nil {
			return err
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
		if !slices.ContainsFunc(burst.Preferences, func(step runspec.PlacementStep) bool { return step.Target == TierCloud }) {
			return authorityError(apierr.Forbidden, "Cloud is outside the sealed burst preference set")
		}
		profile.Class = match.ClassCloud
	}
	preferences, err := burstPreferencesTx(ctx, tx, team, string(unit.RunID), *burst)
	if err != nil {
		return err
	}
	if placementRank(profile, match.Demand{AllowedPools: profile.ReservedPools}, preferences) == len(preferences) {
		return authorityError(apierr.Forbidden, "claimant is outside the sealed burst preference set")
	}
	return nil
}
