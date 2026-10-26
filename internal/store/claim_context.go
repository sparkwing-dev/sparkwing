package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

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
	var request, spec []byte
	var verifiedDefault bool
	q := claimSQL{dialect: tx.dialect}
	var repoKey, repoDisplay, repository, dispatch, subject, principal string
	if err := tx.QueryRowContext(ctx, `SELECT request_json,spec_json,repo_key,repo_display,repo_identity,dispatch,created_subject_kind,created_principal,`+q.verifiedDefaultWork(q.at(q.blob("request_json"), "provenance"))+` FROM runs WHERE team=? AND id=?`, team, runID).Scan(&request, &spec, &repoKey, &repoDisplay, &repository, &dispatch, &subject, &principal, &verifiedDefault); err != nil {
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
		// safety: pool access rules read these, so units sharing a context share pool access.
		Provenance struct {
			Trust           runspec.TrustClass `json:"trust"`
			RepositoryID    int64              `json:"repository_id"`
			Ref             string             `json:"ref"`
			VerifiedDefault bool
		} `json:"provenance"`
	}
	if err := json.Unmarshal(request, &sealed); err != nil {
		return "", err
	}
	sealed.Provenance.VerifiedDefault = verifiedDefault
	if !slices.Contains([]runspec.TrustClass{runspec.TrustVerified, runspec.TrustVerifiedDefault, runspec.TrustSchedule}, sealed.Provenance.Trust) {
		sealed.Provenance.RepositoryID, sealed.Provenance.Ref = 0, ""
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
		RepoKey, RepoDisplay, Repository, Dispatch, Subject, Principal string
		Foreground                                                     bool
	}{kind, requirements, sealed.Request, sealed.Provenance, accepted.Resolved.Settings.Options, accepted.Resolved.Guards, placement, repoKey, repoDisplay, repository, dispatch, subject, principal, sealed.RunID == runID})
	if err != nil {
		return "", err
	}
	var account, minutes any
	if sealed.Request.Burst != nil {
		account, minutes = principal, sealed.Request.Burst.OverflowMinutes
	}
	// Keep the context alive until the referencing source is inserted.
	_, err = tx.execContext(ctx, `INSERT INTO claim_contexts(team,id,requirements,unit_kind,overflow_account,overflow_minutes) VALUES (?,?,?,?,?,?) ON CONFLICT(team,id) DO UPDATE SET id=excluded.id`, team, id, requirements, kind, account, minutes)
	return id, err
}

func retireClaimContextTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef) error {
	query := `SELECT claim_context FROM triggers WHERE team=? AND id=? AND status IN ('done','failed')`
	clear := `UPDATE triggers SET claim_context='' WHERE team=? AND id=?`
	args := []any{team, unit.RunID}
	if unit.Kind == capability.UnitNode {
		query = `SELECT claim_context FROM nodes WHERE team=? AND run_id=? AND node_id=? AND status='done'`
		clear = `UPDATE nodes SET claim_context='' WHERE team=? AND run_id=? AND node_id=?`
		args = append(args, unit.NodeID)
	}
	var id string
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := tx.execContext(ctx, clear, args...); err != nil {
		return err
	}
	return deleteUnusedClaimContextTx(ctx, tx, team, id)
}

func deleteUnusedClaimContextTx(ctx context.Context, tx *storeTx, team Team, id string) error {
	if err := tx.QueryRowContext(ctx, `SELECT id FROM claim_contexts WHERE team=? AND id=?`+tx.forUpdate(), team, id).Scan(&id); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err := tx.execContext(ctx, `DELETE FROM claim_contexts WHERE team=? AND id=? AND NOT EXISTS (SELECT 1 FROM nodes WHERE team=? AND claim_context=?) AND NOT EXISTS (SELECT 1 FROM triggers WHERE team=? AND claim_context=?)`, team, id, team, id, team, id)
	return err
}
