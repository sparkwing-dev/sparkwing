package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// CreateAuthorizedDebugPause commits a masked pause under live run-control authority.
func (t *Tenant) CreateAuthorizedDebugPause(ctx context.Context, a capability.Authorized, job, reason string, expires time.Time, key string) error {
	if strings.TrimSpace(job) == "" || strings.TrimSpace(reason) == "" {
		return ErrInvalidInput
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		expires, reason := expires, reason
		target := a.Target
		if target.NodeID != "" && target.NodeID != job {
			return bindingRefusal()
		}
		p, err := t.runControlAuthorityTx(ctx, tx, a, "/api/v1/runs/{run}/debug-pauses")
		if err != nil {
			return err
		}
		replay, err := runControlReplayTx(ctx, tx, t.team, target.RunID, p, "pause", key, struct {
			Job, Reason string
			Expires     time.Time
		}{job, reason, expires}, nil)
		if err != nil {
			return err
		}
		if replay {
			_, err = t.runControlAuthorityTx(ctx, tx, a, "/api/v1/runs/{run}/debug-pauses")
			return err
		}
		run, err := readRunTx(ctx, tx, t.team, target.RunID)
		if err != nil {
			return err
		}
		row, err := readNodeTx(ctx, tx, t.team, NodeRef{target.RunID, job})
		if err != nil {
			return err
		}
		if run.FinishedAt != nil || run.CancelRequestedAt != nil || row.Status == StatusDone || row.CancelRequestedAt != nil {
			return authorityError(apierr.IllegalTransition, "job cannot be paused")
		}
		now := tx.now()
		if expires.IsZero() {
			expires = now.Add(limits.DebugPause)
		}
		if !expires.After(now) {
			return authorityError(apierr.BadRequest, "pause expiry must be in the future")
		}
		reason, err = controlTextTx(ctx, tx, t.team, run, reason)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO debug_pauses(team,run_id,node_id,reason,paused_at,expires_at) VALUES (?,?,?,?,?,?) ON CONFLICT(team,run_id,node_id,reason) DO UPDATE SET paused_at=excluded.paused_at,expires_at=excluded.expires_at,released_at=NULL,released_by='',release_kind=''`, t.team, target.RunID, job, reason, now.UnixNano(), expires.UnixNano()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE nodes SET status_detail='paused' WHERE team=? AND run_id=? AND node_id=?`, t.team, target.RunID, job); err != nil {
			return err
		}
		if _, err = appendEventTx(ctx, tx, t.team, target.RunID, job, "debug.paused", map[string]any{"reason": reason, "expires_at": expires}, now); err != nil {
			return err
		}
		_, err = t.runControlAuthorityTx(ctx, tx, a, "/api/v1/runs/{run}/debug-pauses")
		return err
	})
}

// ResolveAuthorizedApproval commits one human decision and its replay under current authority.
func (t *Tenant) ResolveAuthorizedApproval(ctx context.Context, a capability.Authorized, decision controllerwire.ApprovalDecision) (controllerwire.ApprovalResult, error) {
	var out controllerwire.ApprovalResult
	if decision.Resolution != "approved" && decision.Resolution != "denied" {
		return out, ErrInvalidInput
	}
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		out = controllerwire.ApprovalResult{}
		if a.Target.NodeID == "" {
			return bindingRefusal()
		}
		p, err := t.runControlAuthorityTx(ctx, tx, a, "/api/v1/runs/{run}/approvals/{job}")
		if err != nil {
			return err
		}
		body := struct{ Job, Resolution, Comment string }{a.Target.NodeID, decision.Resolution, decision.Comment}
		replay, err := runControlReplayTx(ctx, tx, t.team, a.Target.RunID, p, "approval", decision.IdempotencyKey, body, &out)
		if err != nil {
			return err
		}
		if replay {
			if _, err := t.runControlAuthorityTx(ctx, tx, a, "/api/v1/runs/{run}/approvals/{job}"); err != nil {
				return err
			}
			out.Replayed = true
			return nil
		}
		run, err := readRunTx(ctx, tx, t.team, a.Target.RunID)
		if err != nil {
			return err
		}
		if run.FinishedAt != nil || run.CancelRequestedAt != nil {
			return authorityError(apierr.IllegalTransition, "run is no longer executable")
		}
		comment, err := controlTextTx(ctx, tx, t.team, run, decision.Comment)
		if err != nil {
			return err
		}
		to := NodeFailed
		if decision.Resolution == "approved" {
			to = NodeSuccess
		}
		change := NodeTransition{Ref: NodeRef{a.Target.RunID, a.Target.NodeID}, To: to, Cause: CauseApprovalResolve, Actor: Actor{Kind: ActorOperator, Authority: p.Authority}, Extra: TransitionExtra{Approval: &ApprovalResolution{Decision: decision.Resolution, Comment: comment}}}
		if _, err = transitionNodeTx(ctx, tx, t.team, change, tx.now()); err != nil {
			return err
		}
		gate, err := scanApproval(tx.QueryRowContext(ctx, `SELECT run_id,node_id,requested_at,message,timeout_ms,on_timeout,approver,resolved_at,resolution,comment FROM approvals WHERE team=? AND run_id=? AND node_id=?`, t.team, a.Target.RunID, a.Target.NodeID))
		if err != nil {
			return err
		}
		out.Approval = controllerwire.Approval{RunID: gate.RunID, JobID: gate.NodeID, RequestedAt: gate.RequestedAt, Message: gate.Message, TimeoutMS: gate.TimeoutMS, OnTimeout: gate.OnTimeout, ResolvedAt: gate.ResolvedAt, Resolution: gate.Resolution, Approver: p.Capability.Subject.ID, Comment: gate.Comment}
		encoded, err := json.Marshal(out)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE run_control_requests SET result_json=? WHERE team=? AND run_id=? AND subject_kind=? AND subject_id=? AND operation='approval' AND request_key=?`, encoded, t.team, a.Target.RunID, p.Capability.Subject.Kind, p.Capability.Subject.ID, decision.IdempotencyKey); err != nil {
			return err
		}
		_, err = t.runControlAuthorityTx(ctx, tx, a, "/api/v1/runs/{run}/approvals/{job}")
		return err
	})
	if err != nil {
		return controllerwire.ApprovalResult{}, err
	}
	return out, nil
}

func (t *Tenant) runControlAuthorityTx(ctx context.Context, tx *storeTx, a capability.Authorized, path string) (capability.Principal, error) {
	if a.Target.Team != string(t.team) || a.Target.RunID == "" {
		return capability.Principal{}, bindingRefusal()
	}
	p, err := tx.ResolveAuthority(ctx, a.Principal.Authority)
	if err != nil {
		return p, err
	}
	route, ok := capability.LookupRoute("POST", path)
	if !ok || len(route.Access) != 1 || route.Access[0].Liveness != capability.LiveCredential {
		return p, authorityError(apierr.PolicyMissing, "run control policy is unavailable")
	}
	_, err = capability.Authorize(ctx, tx, route, capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, a.Target)
	if err != nil {
		return p, err
	}
	return p, lockStateRunTx(ctx, tx, t.team, a.Target.RunID)
}

func runControlReplayTx(ctx context.Context, tx *storeTx, team Team, run string, p capability.Principal, operation, key string, body, out any) (bool, error) {
	if strings.TrimSpace(key) == "" {
		return false, ErrInvalidInput
	}
	digest, err := stateHash(body)
	if err != nil {
		return false, err
	}
	var previous string
	var result []byte
	err = tx.QueryRowContext(ctx, `SELECT request_hash,result_json FROM run_control_requests WHERE team=? AND run_id=? AND subject_kind=? AND subject_id=? AND operation=? AND request_key=?`, team, run, p.Capability.Subject.Kind, p.Capability.Subject.ID, operation, key).Scan(&previous, &result)
	if err == nil {
		if previous != digest {
			return false, authorityError(apierr.Conflict, "idempotency key has different run control inputs")
		}
		if out != nil {
			err = json.Unmarshal(result, out)
		}
		return true, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_control_requests(team,run_id,subject_kind,subject_id,operation,request_key,request_hash,result_json) VALUES (?,?,?,?,?,?,?,?)`, team, run, p.Capability.Subject.Kind, p.Capability.Subject.ID, operation, key, digest, []byte(`{}`))
	return false, err
}

func controlTextTx(ctx context.Context, tx *storeTx, team Team, run RunRow, text string) (string, error) {
	spec := runspec.Spec{Request: run.Request}
	if run.Spec != nil {
		spec = *run.Spec
	} else {
		input := run.Request.Request
		spec.Resolved.Args = input.RequestedArgs
		spec.Resolved.SecretArgs = input.SecretArgs.Value
		if !input.SecretArgs.Set {
			for name := range input.RequestedArgs {
				spec.Resolved.SecretArgs = append(spec.Resolved.SecretArgs, name)
			}
		}
	}
	values, err := planSecretsTx(ctx, tx, team, spec)
	if err != nil {
		return "", err
	}
	masker := secrets.NewMasker()
	for _, value := range values {
		masker.Register(value)
	}
	if !utf8.ValidString(text) {
		return "", authorityError(apierr.BadRequest, "control text must be UTF-8")
	}
	return masker.Mask(text), nil
}
