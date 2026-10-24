package store

import (
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type claimContextTerms struct {
	Value []string `json:"value"`
}

type claimContextPlacement struct {
	Requires claimContextTerms `json:"requires"`
	Prefers  claimContextTerms `json:"prefers"`
}

// A context shares only immutable policy inputs; readiness and authority stay in the claim predicate.
func persistClaimContextTx(ctx context.Context, tx *storeTx, team Team, runID string, kind capability.UnitKind, requirements string, execution []byte) (string, error) {
	var request, spec, pools []byte
	var repoKey, repoDisplay, repository, dispatch, subject, principal string
	if err := tx.QueryRowContext(ctx, `SELECT request_json,spec_json,allowed_pools,repo_key,repo_display,repo_identity,dispatch,created_subject_kind,created_principal FROM runs WHERE team=? AND id=?`, team, runID).Scan(&request, &spec, &pools, &repoKey, &repoDisplay, &repository, &dispatch, &subject, &principal); err != nil {
		return "", err
	}
	var sealed struct {
		RunID   string `json:"run_id"`
		Request struct {
			Mode     runspec.IntakeMode      `json:"mode"`
			Dispatch runspec.Dispatch        `json:"dispatch"`
			Burst    *runspec.BurstPlacement `json:"burst"`
			Options  struct {
				Runner   runspec.Optional[runspec.RunnerSelection] `json:"runner"`
				Requires claimContextTerms                         `json:"requires"`
			} `json:"options"`
			Git struct {
				Repository struct {
					Display string `json:"display"`
				} `json:"repository"`
			} `json:"git"`
			OriginMachine   string `json:"origin_machine"`
			LocalForeground bool   `json:"local_foreground"`
		} `json:"request"`
		Provenance struct {
			Trust runspec.TrustClass `json:"trust"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(request, &sealed); err != nil {
		return "", err
	}
	var accepted struct {
		Resolved struct {
			Settings struct {
				Options *claimContextPlacement `json:"options"`
			} `json:"settings"`
			Guards runspec.Guards `json:"guards"`
		} `json:"resolved"`
	}
	if len(spec) > 0 {
		if err := json.Unmarshal(spec, &accepted); err != nil {
			return "", err
		}
	}
	var placement claimContextPlacement
	if len(execution) > 0 {
		if err := json.Unmarshal(execution, &placement); err != nil {
			return "", err
		}
	}
	// Missing resolved options retain request fallback; an explicit empty object does not.
	id, err := stateHash(struct {
		Kind                                                           capability.UnitKind
		Requirements                                                   string
		Request                                                        any
		Provenance                                                     any
		Options                                                        *claimContextPlacement
		Guards                                                         runspec.Guards
		Execution                                                      claimContextPlacement
		Pools                                                          json.RawMessage
		RepoKey, RepoDisplay, Repository, Dispatch, Subject, Principal string
		Foreground                                                     bool
	}{kind, requirements, sealed.Request, sealed.Provenance, accepted.Resolved.Settings.Options, accepted.Resolved.Guards, placement, pools, repoKey, repoDisplay, repository, dispatch, subject, principal, sealed.RunID == runID})
	if err != nil {
		return "", err
	}
	_, err = tx.execContext(ctx, `INSERT INTO claim_contexts(team,id,requirements,unit_kind) VALUES (?,?,?,?) ON CONFLICT(team,id) DO NOTHING`, team, id, requirements, kind)
	return id, err
}
