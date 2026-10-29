package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func validateResultProofTx(ctx context.Context, tx *storeTx, team Team, actor runstate.Actor, ref runspec.NodeRef, attempt int64) error {
	proof := actor.Result
	if proof == nil {
		return nil
	}
	if proof.Actor != actor.Authority || proof.Method != http.MethodPost || runspec.RunID(proof.RunID) != ref.RunID || runspec.NodeID(proof.NodeID) != ref.NodeID || proof.ClaimID != "" || runspec.ClaimGeneration(proof.Generation) != actor.Generation || len(proof.Digest) != 64 {
		return holderRefusal()
	}
	if _, err := hex.DecodeString(proof.Digest); err != nil {
		return holderRefusal()
	}
	expected := "/api/v1/runs/{run}/finish"
	if ref.NodeID != "" {
		expected = "/api/v1/runs/{run}/jobs/{job}/transition"
	}
	privatePattern := "/api/v1/runs/{run}/local-host/nodes/{job}/transition"
	if ref.NodeID == "" {
		privatePattern = "/api/v1/runs/{run}/local-host/finish"
	}
	if proof.Pattern != expected && proof.Pattern != privatePattern && !(ref.NodeID == "" && (proof.Pattern == "/api/v1/runs/{run}/plan" || proof.Pattern == "/api/v1/runs/{run}/local-host/transition")) {
		return holderRefusal()
	}
	p, err := resolveTeamAuthorityTx(ctx, tx, team, actor.Authority, sameTeam)
	if err != nil {
		return err
	}
	c := p.Capability
	if c.Unit != capability.UnitNode {
		attempt = c.Binding.Attempt
	}
	if runspec.RunID(c.Binding.RunID) != ref.RunID || c.Binding.Generation != proof.Generation || c.Binding.Holder != actor.Holder || actor.CapabilityPrefix != c.Prefix || proof.Attempt != attempt {
		return holderRefusal()
	}
	return nil
}

func persistPlanResultProofTx(ctx context.Context, tx *storeTx, team Team, request PlanAcceptance) error {
	if request.Actor.Result == nil {
		return nil
	}
	if err := validateResultProofTx(ctx, tx, team, request.Actor, runspec.NodeRef{RunID: runspec.RunID(request.RunID)}, 0); err != nil {
		return err
	}
	if request.Actor.Result.Pattern != "/api/v1/runs/{run}/plan" {
		return holderRefusal()
	}
	_, err := tx.execContext(ctx, `UPDATE capabilities SET result_digest=? WHERE team=? AND prefix=? AND result_digest=''`, request.Actor.Result.Digest, team, request.Actor.CapabilityPrefix)
	return err
}

func authenticationResultTx(ctx context.Context, tx *storeTx, c capability.Capability, evidence capability.RequestEvidence, out *capability.BindingState) error {
	if evidence.ResultDigest == "" || c.Binding.RunID == "" {
		return nil
	}
	if evidence.Method == http.MethodPost && evidence.Pattern == "/api/v1/runs/{run}/plan" {
		var digest string
		if err := tx.QueryRowContext(ctx, `SELECT result_digest FROM capabilities WHERE team=? AND prefix=?`, c.Team, c.Prefix).Scan(&digest); err != nil {
			return err
		}
		if digest != "" {
			out.Result = &capability.RecordedResult{Actor: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}, Method: "POST", Pattern: evidence.Pattern, RunID: c.Binding.RunID, Generation: c.Binding.Generation, Attempt: c.Binding.Attempt, Digest: digest}
		}
		return nil
	}
	var raw []byte
	query := `SELECT state_actor FROM runs WHERE team=? AND id=?`
	args := []any{c.Team, c.Binding.RunID}
	if c.Binding.NodeID != "" {
		query = `SELECT state_actor FROM nodes WHERE team=? AND run_id=? AND node_id=?`
		args = append(args, c.Binding.NodeID)
	}
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		return err
	}
	var actor runstate.Actor
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &actor); err != nil {
			return err
		}
	}
	out.Result = actor.Result
	return nil
}
