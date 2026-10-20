package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

const sealedRotationRunPage = 200

// SealedRotation counts the envelopes an operator rotation resealed and names those no configured key opened.
type SealedRotation struct {
	Secrets         int             `json:"secrets"`
	GitCredentials  int             `json:"git_credentials"`
	RunEnvironments int             `json:"run_environments"`
	Settings        int             `json:"settings"`
	PreviousKeyOnly int             `json:"previous_key_only"`
	Unreadable      []SealedRowName `json:"unreadable"`
}

// SealedRotationCipher reseals bound envelopes and opens them under the current key alone.
type SealedRotationCipher interface {
	SessionKeyCipher
	OpenBoundCurrent(purpose secrets.Purpose, team, name, scope string, shared, masked bool, envelope string) (string, error)
}

// SealedRowName identifies a sealed row a rotation left unchanged.
type SealedRowName struct {
	Purpose  secrets.Purpose `json:"purpose"`
	Team     Team            `json:"team,omitempty"`
	Name     string          `json:"name"`
	Pipeline string          `json:"pipeline,omitempty"`
}

type sealedRotationTx struct {
	tx     *storeTx
	cipher SealedRotationCipher
	verify bool
	out    SealedRotation
}

var errSealedUnreadable = errors.New("sealed value opens under no configured key")

func (r *sealedRotationTx) reseal(purpose secrets.Purpose, team Team, name, scope string, shared, masked bool, envelope string) (string, error) {
	plain, err := r.cipher.OpenBoundCurrent(purpose, string(team), name, scope, shared, masked, envelope)
	if err != nil && !errors.Is(err, secrets.ErrKeyRefused) {
		plain, err = r.cipher.OpenBound(purpose, string(team), name, scope, shared, masked, envelope)
		if err == nil {
			r.out.PreviousKeyOnly++
		}
	}
	if err != nil && !errors.Is(err, secrets.ErrKeyRefused) {
		r.out.Unreadable = append(r.out.Unreadable, SealedRowName{Purpose: purpose, Team: team, Name: name, Pipeline: scopeName(purpose, scope)})
		return "", errSealedUnreadable
	}
	if err != nil || r.verify {
		return "", err
	}
	return r.cipher.SealBound(purpose, string(team), name, scope, shared, masked, plain)
}

func scopeName(purpose secrets.Purpose, scope string) string {
	if purpose == secrets.PurposeSecret {
		return scope
	}
	return ""
}

// RotateSealedValues reseals every tenant's secrets, git credentials and submitted run environments, and the
// session signing key, under the cipher's current key. Each step commits on its own and repeats safely.
// Verify only counts, reporting how many rows still open under the previous key alone.
func (o *Operator) RotateSealedValues(ctx context.Context, a capability.Authorized, cipher SealedRotationCipher, verify bool) (SealedRotation, error) {
	if cipher == nil {
		return SealedRotation{}, authorityError(apierr.Unavailable, "bound secret encryption is unavailable")
	}
	total := SealedRotation{Unreadable: []SealedRowName{}}
	step := func(fn func(*sealedRotationTx) error) error {
		var done SealedRotation
		err := o.s.inTx(ctx, func(tx *storeTx) error {
			if err := sealedRotationAuthorityTx(ctx, tx, a); err != nil {
				return err
			}
			r := &sealedRotationTx{tx: tx, cipher: cipher, verify: verify}
			if err := fn(r); err != nil {
				return err
			}
			done = r.out
			return nil
		})
		if err != nil {
			return err
		}
		total.Secrets += done.Secrets
		total.GitCredentials += done.GitCredentials
		total.RunEnvironments += done.RunEnvironments
		total.Settings += done.Settings
		total.PreviousKeyOnly += done.PreviousKeyOnly
		total.Unreadable = append(total.Unreadable, done.Unreadable...)
		return nil
	}
	var teams []Team
	if err := step(func(r *sealedRotationTx) (err error) {
		teams, err = rotationTeamsTx(ctx, r.tx)
		return err
	}); err != nil {
		return SealedRotation{}, err
	}
	for _, team := range teams {
		if err := step(func(r *sealedRotationTx) error { return r.tenantCredentials(ctx, &Tenant{s: o.s, team: team}) }); err != nil {
			return SealedRotation{}, err
		}
		for after, more := "", true; more; {
			if err := step(func(r *sealedRotationTx) (err error) {
				after, more, err = r.runEnvironments(ctx, team, after)
				return err
			}); err != nil {
				return SealedRotation{}, err
			}
		}
	}
	if err := step(func(r *sealedRotationTx) error { return r.sessionKey(ctx) }); err != nil {
		return SealedRotation{}, err
	}
	return total, nil
}

func sealedRotationAuthorityTx(ctx context.Context, tx *storeTx, a capability.Authorized) error {
	route, ok := capability.LookupRoute("POST", "/api/v1/operator/secrets/rotate")
	if !ok || !reflect.DeepEqual(a.Target, capability.Target{}) {
		return bindingRefusal()
	}
	selected := -1
	for i, rule := range route.Access {
		if reflect.DeepEqual(rule, a.Access) {
			selected = i
		}
	}
	if selected < 0 {
		return bindingRefusal()
	}
	p, err := resolveProvisionAuthorityTx(ctx, tx, a.Principal.Authority, capability.ProofForConsole(a.Principal))
	if err != nil {
		return err
	}
	_, err = capability.Authorize(ctx, tx, route, capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: selected}}}, capability.Target{})
	return err
}

func rotationTeamsTx(ctx context.Context, tx *storeTx) (_ []Team, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM teams ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []Team
	for rows.Next() {
		var team Team
		if err := rows.Scan(&team); err != nil {
			return nil, err
		}
		out = append(out, team)
	}
	return out, rows.Err()
}

func (r *sealedRotationTx) tenantCredentials(ctx context.Context, t *Tenant) error {
	if err := t.lockTeamTx(ctx, r.tx); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	rows, err := t.credentialSecretsTx(ctx, r.tx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		value, err := r.reseal(secrets.PurposeSecret, t.team, row.Name, row.Pipeline, row.Shared, row.Masked, row.Value)
		if errors.Is(err, errSealedUnreadable) {
			continue
		}
		if err != nil {
			return err
		}
		r.out.Secrets++
		if r.verify {
			continue
		}
		if _, err := r.tx.ExecContext(ctx, `UPDATE secrets SET value=? WHERE team=? AND name=? AND pipeline=?`, value, t.team, row.Name, row.Pipeline); err != nil {
			return err
		}
	}
	credentials, err := t.credentialGitTx(ctx, r.tx)
	if err != nil {
		return err
	}
	for _, row := range credentials {
		value, err := r.reseal(secrets.PurposeGitCredential, t.team, "git-credential:"+row.Host, row.CredentialScope(), false, true, row.Secret)
		if errors.Is(err, errSealedUnreadable) {
			continue
		}
		if err != nil {
			return err
		}
		r.out.GitCredentials++
		if r.verify {
			continue
		}
		if _, err := r.tx.ExecContext(ctx, `UPDATE git_credentials SET secret=? WHERE team=? AND id=?`, value, t.team, row.ID); err != nil {
			return err
		}
	}
	return nil
}

func (r *sealedRotationTx) runEnvironments(ctx context.Context, team Team, after string) (string, bool, error) {
	rows, err := r.tx.QueryContext(ctx, `SELECT id,submitted_env FROM runs WHERE team=? AND submitted_env<>'' AND id>? ORDER BY id LIMIT ?`+r.tx.forUpdate(), team, after, sealedRotationRunPage)
	if err != nil {
		return after, false, err
	}
	type envelope struct{ run, value string }
	var page []envelope
	for rows.Next() {
		var e envelope
		if err := rows.Scan(&e.run, &e.value); err != nil {
			return after, false, errors.Join(err, rows.Close())
		}
		page = append(page, e)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return after, false, err
	}
	for _, e := range page {
		value, err := r.reseal(secrets.PurposeRunEnvironment, team, e.run, "run-environment", false, true, e.value)
		if errors.Is(err, errSealedUnreadable) {
			continue
		}
		if err != nil {
			return after, false, err
		}
		r.out.RunEnvironments++
		if r.verify {
			continue
		}
		if _, err := r.tx.ExecContext(ctx, `UPDATE runs SET submitted_env=? WHERE team=? AND id=?`, value, team, e.run); err != nil {
			return after, false, err
		}
	}
	if len(page) == 0 {
		return after, false, nil
	}
	return page[len(page)-1].run, len(page) == sealedRotationRunPage, nil
}

func (r *sealedRotationTx) sessionKey(ctx context.Context) error {
	var envelope []byte
	err := r.tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=? AND sealed=1`+r.tx.forUpdate(), metaKeySessionCSRFKey).Scan(&envelope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	value, err := r.reseal(secrets.PurposeSetting, "", metaKeySessionCSRFKey, "settings", false, true, string(envelope))
	if errors.Is(err, errSealedUnreadable) {
		return nil
	}
	if err != nil {
		return err
	}
	r.out.Settings++
	if r.verify {
		return nil
	}
	_, err = r.tx.ExecContext(ctx, `UPDATE settings SET value=? WHERE key=? AND sealed=1`, []byte(value), metaKeySessionCSRFKey)
	return err
}
