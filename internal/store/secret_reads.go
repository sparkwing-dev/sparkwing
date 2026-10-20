package store

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/secretname"
)

// ReadSecret selects a secret under current authority and commits each claim release audit.
func (t *Tenant) ReadSecret(ctx context.Context, a capability.Authorized) (*Secret, error) {
	var out *Secret
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockClaimControllerTx(ctx, tx); err != nil {
			return err
		}
		p, err := t.authorizeSecretReadTx(ctx, tx, a)
		if err != nil {
			return err
		}
		if err := secretname.Validate(a.Target.SecretName); err != nil {
			return ErrInvalidInput
		}
		claim := a.Access.Binding == capability.BindDeclaredSecret
		run := a.Target.RunID
		selection := secretSelection{scope: secretPipelineScope, pipeline: a.Target.Pipeline, includeUnshared: true}
		if claim {
			run = p.Capability.Binding.RunID
		}
		var selected RunRow
		if run != "" {
			if selected, err = readRunTx(ctx, tx, t.team, run); err != nil {
				return err
			}
			// safety: a run-bound read selects the pipeline the run speaks for; a caller's selector cannot widen it.
			if selection, err = runSecretSelection(selected.Request); err != nil {
				return err
			}
		}
		if claim && slices.Contains(selected.Request.Request.LocalSecretNames, a.Target.SecretName) {
			var value string
			value, err = tx.store.localSecret(t.team, run, a.Target.SecretName)
			out = &Secret{Name: a.Target.SecretName, Value: value, Masked: true, Ephemeral: true, Pipeline: selection.pipeline, Principal: selected.Request.Request.Actor.ID}
		} else {
			out, _, err = selectedSecretTx(ctx, tx, t.team, a.Target.SecretName, selection)
		}
		if err != nil {
			return err
		}
		if !claim && out.Masked {
			return bindingRefusal()
		}
		if claim {
			b := p.Capability.Binding
			if _, err := appendEventTx(ctx, tx, t.team, b.RunID, b.NodeID, "secret_released", map[string]any{"name": out.Name, "generation": b.Generation}, tx.now()); err != nil {
				return err
			}
		}
		_, err = t.authorizeSecretReadTx(ctx, tx, a)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (t *Tenant) authorizeSecretReadTx(ctx context.Context, tx *storeTx, a capability.Authorized) (capability.Principal, error) {
	chain, err := authorityChainTx(ctx, tx, a.Principal.Authority, true)
	if err != nil {
		return capability.Principal{}, err
	}
	states := make([]capability.AuthorityState, len(chain))
	for i, node := range chain {
		states[i] = capability.AuthorityState{Principal: node.principal}
	}
	p, err := capability.ValidateAuthority(states, tx.now())
	if err != nil {
		return capability.Principal{}, err
	}
	if a.Target.Team != string(t.team) || p.Capability.Team != string(t.team) {
		return capability.Principal{}, bindingRefusal()
	}
	route, ok := capability.LookupRoute("GET", "/api/v1/secrets/{name}")
	if !ok {
		return capability.Principal{}, bindingRefusal()
	}
	for i, rule := range route.Access {
		if rule.Credential != a.Access.Credential || rule.Binding != a.Access.Binding || rule.Liveness != a.Access.Liveness || !slices.Equal(rule.Grants, a.Access.Grants) {
			continue
		}
		if rule.Binding == capability.BindDeclaredSecret {
			if err := checkBurstClaimantTx(ctx, tx, t.team, chain, UnitRef{Kind: p.Capability.Unit, RunID: p.Capability.Binding.RunID, NodeID: p.Capability.Binding.NodeID}); err != nil {
				return capability.Principal{}, err
			}
			if err := t.secretClaimFenceTx(ctx, tx, p.Capability); err != nil {
				return capability.Principal{}, err
			}
		}
		auth := capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: i}}}
		_, err := capability.Authorize(ctx, tx, route, auth, a.Target)
		return p, err
	}
	return capability.Principal{}, bindingRefusal()
}

func (t *Tenant) secretClaimFenceTx(ctx context.Context, tx *storeTx, c capability.Capability) error {
	b := c.Binding
	if c.UnitID == "" || b.RunID == "" || b.Expansion != nil || c.Delivery == capability.DeliveryBootstrap || !slices.Contains([]capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitNode}, c.Unit) {
		return bindingRefusal()
	}
	if err := lockClaimOwnerTx(ctx, tx, t.team, UnitRef{Kind: c.Unit, RunID: b.RunID, NodeID: b.NodeID}); err != nil {
		return err
	}
	if err := lockStateRunTx(ctx, tx, t.team, b.RunID); err != nil {
		return err
	}
	if c.Unit == capability.UnitNode {
		var node string
		if err := tx.QueryRowContext(ctx, `SELECT node_id FROM nodes WHERE team=? AND run_id=? AND node_id=?`+tx.forUpdate(), t.team, b.RunID, b.NodeID).Scan(&node); err != nil {
			return err
		}
	}
	state, err := authenticationBindingTx(ctx, tx, c)
	if err != nil {
		return err
	}
	if state.RunCancelled || state.RunFinished || state.FinishedAt != nil || b.Generation <= 0 || b.Holder == "" || state.Current.Generation != b.Generation || state.Current.Holder != b.Holder || b.Attempt > 0 && state.Current.Attempt != b.Attempt || !state.LeaseExpiresAt.After(tx.now()) {
		return bindingRefusal()
	}
	var deadline sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM triggers WHERE team=? AND id=?`, t.team, b.RunID).Scan(&deadline); err != nil {
		return err
	}
	if deadline.Valid && deadline.Int64 <= tx.now().UnixNano() {
		return bindingRefusal()
	}
	return nil
}

type selectedSecretRow struct {
	secret Secret
	pool   string
}

func selectedSecretsTx(ctx context.Context, tx *storeTx, team Team, name string, selection secretSelection) ([]selectedSecretRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name,value,principal,pipeline,masked,shared,created_at,updated_at,only_on FROM secrets WHERE team=? AND (?='' OR name=?) AND ((?=1 AND pipeline<>'' AND pipeline=?) OR (?=1 AND pipeline='' AND (shared=1 OR ?=1))) ORDER BY name,CASE WHEN pipeline='' THEN 1 ELSE 0 END`+tx.forShare(), team, name, name, boolInt(selection.scope == secretPipelineScope), selection.pipeline, boolInt(selection.scope != secretNone), boolInt(selection.includeUnshared))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []selectedSecretRow
	for rows.Next() {
		var row selectedSecretRow
		var masked, shared int
		var created, updated int64
		if err := rows.Scan(&row.secret.Name, &row.secret.Value, &row.secret.Principal, &row.secret.Pipeline, &masked, &shared, &created, &updated, &row.pool); err != nil {
			return nil, err
		}
		if len(out) > 0 && out[len(out)-1].secret.Name == row.secret.Name {
			continue
		}
		row.secret.Masked, row.secret.Shared = masked != 0, shared != 0
		row.secret.CreatedAt, row.secret.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
		out = append(out, row)
	}
	return out, rows.Err()
}

func selectedSecretTx(ctx context.Context, tx *storeTx, team Team, name string, selection secretSelection) (*Secret, string, error) {
	rows, err := selectedSecretsTx(ctx, tx, team, name, selection)
	if err != nil {
		return nil, "", err
	}
	if len(rows) == 0 {
		return nil, "", ErrNotFound
	}
	return &rows[0].secret, rows[0].pool, nil
}
