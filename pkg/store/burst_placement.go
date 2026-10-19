package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
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
		case "here":
			target = "class=coordinator"
		case "machines":
			target = "name="
		case "cloud":
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
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT prefix,subject_id FROM capabilities WHERE team=? AND created_by=? AND subject_kind='machine' AND bind_kind='none' AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>?)`, team, account, tx.now().UnixNano())
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
		record, err := loadCapabilityRecordTx(ctx, tx, prefix)
		if err != nil {
			return nil, err
		}
		if record.Origin != nil && record.Origin.Personal {
			names = append(names, name)
		}
	}
	return names, nil
}

func checkPersonalMachineTx(ctx context.Context, tx *storeTx, chain []authorityNode, run string, request runspec.Request) error {
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
	if automatedRun(request) {
		var armer string
		if err := tx.QueryRowContext(ctx, `SELECT created_principal FROM runs WHERE team=? AND id=?`, root.Team, run).Scan(&armer); err != nil {
			return err
		}
		return personalAutomatedRunTx(ctx, tx, Team(root.Team), root.Prefix, owner, armer, request)
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

// safety: the only rule that admits a personal machine to an automated run:
// its owner owns the run, an admin approved it and the owner can read every repository.
func personalAutomatedRunTx(ctx context.Context, tx *storeTx, team Team, prefix, owner, requester string, request runspec.Request) error {
	refused := authorityError(apierr.Forbidden, "personal machine takes only its owner's automated runs after admin approval")
	var account string
	var err error
	switch request.Origin {
	case runspec.OriginSchedule:
		err = tx.QueryRowContext(ctx, `SELECT account_id FROM memberships WHERE team=? AND account_id=?`, team, requester).Scan(&account)
	case runspec.OriginApp:
		if !request.Git.App.Set || request.Git.App.Value.AuthorID <= 0 {
			return refused
		}
		err = tx.QueryRowContext(ctx, `SELECT i.account_id FROM identities i JOIN memberships m ON m.account_id=i.account_id AND m.team=? WHERE i.provider=? AND i.subject=?`, team, ProviderGitHub, strconv.FormatInt(request.Git.App.Value.AuthorID, 10)).Scan(&account)
	}
	if errors.Is(err, sql.ErrNoRows) || err == nil && (account == "" || account != owner) {
		return refused
	}
	if err != nil {
		return err
	}
	var approved int64
	err = tx.QueryRowContext(ctx, `SELECT approved_at FROM machine_automation WHERE team=? AND prefix=? AND owner=?`, team, prefix, owner).Scan(&approved)
	if errors.Is(err, sql.ErrNoRows) || err == nil && approved <= 0 {
		return refused
	}
	if err != nil {
		return err
	}
	repos, err := runReposTx(ctx, tx, team, request)
	if err != nil || len(repos) == 0 {
		return errors.Join(err, refused)
	}
	for _, repo := range repos {
		var allowed int64
		err := tx.QueryRowContext(ctx, `SELECT allowed FROM machine_repo_access WHERE team=? AND prefix=? AND repo=? AND checked_at>?`, team, prefix, repo, tx.now().Add(-limits.PersonalRepoAccess).UnixNano()).Scan(&allowed)
		if errors.Is(err, sql.ErrNoRows) || err == nil && allowed != 1 {
			return refused
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func runReposTx(ctx context.Context, tx *storeTx, team Team, request runspec.Request) ([]string, error) {
	repo := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(request.Git.Repository.Display, "https://"), "github.com/"), ".git"))
	raw, keyed := strings.CutPrefix(string(request.Git.Repository.Key), "gh:")
	id, err := strconv.ParseInt(raw, 10, 64)
	if _, ok := ParseGitHubRepo(repo); !ok || !keyed || err != nil {
		return nil, nil
	}
	repos := []string{repo}
	rows, err := tx.QueryContext(ctx, `SELECT extra_repo FROM github_app_extra_repos WHERE team=? AND repository_id=? ORDER BY extra_repo`, team, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var extra string
		if err := rows.Scan(&extra); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		repos = append(repos, strings.ToLower(extra))
	}
	return repos, errors.Join(rows.Err(), rows.Close())
}

func checkBurstClaimantTx(ctx context.Context, tx *storeTx, team Team, chain []authorityNode, unit UnitRef) error {
	seal, err := sealedRequestTx(ctx, tx, team, unit.RunID)
	burst := seal.Request.Burst
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
			if err := checkPersonalMachineTx(ctx, tx, chain, unit.RunID, seal.Request); err != nil {
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
