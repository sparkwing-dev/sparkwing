package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

const sealedRotationRunPage = 200

// SealedRotation counts the envelopes an operator rotation resealed and names those no configured key opened.
type SealedRotation struct {
	AlreadySealed   int `json:"already_sealed"`
	Secrets         int `json:"secrets"`
	GitCredentials  int `json:"git_credentials"`
	RunEnvironments int `json:"run_environments"`
	// RunArguments counts resealed run argument envelopes; PlaintextRunArguments counts
	// run and schedule documents written before argument sealing, which rotation seals and verify only counts.
	RunArguments          int `json:"run_arguments"`
	PlaintextRunArguments int `json:"plaintext_run_arguments"`
	Settings              int `json:"settings"`
	// RunEventSubscriptions counts resealed subscription signing secrets.
	RunEventSubscriptions int             `json:"run_event_subscriptions"`
	PreviousKeyOnly       int             `json:"previous_key_only"`
	Unreadable            []SealedRowName `json:"unreadable"`
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
	tx            *storeTx
	cipher        SealedRotationCipher
	verify        bool
	plaintextOnly bool
	out           SealedRotation
}

var errSealedUnchanged = errors.New("sealed value left unchanged")

var errSealedUnreadable = errors.New("sealed value opens under no configured key")

func (r *sealedRotationTx) reseal(purpose secrets.Purpose, team Team, name, scope string, shared, masked bool, envelope string) (string, error) {
	plain, err := r.cipher.OpenBoundCurrent(purpose, string(team), name, scope, shared, masked, envelope)
	if err == nil && r.plaintextOnly {
		r.out.AlreadySealed++
		return "", errSealedUnchanged
	}
	if err != nil && !errors.Is(err, secrets.ErrKeyRefused) {
		plain, err = r.cipher.OpenBound(purpose, string(team), name, scope, shared, masked, envelope)
		if err == nil {
			r.out.PreviousKeyOnly++
			if r.plaintextOnly {
				return "", errSealedUnchanged
			}
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

// RotateSealedValues reseals every tenant's secrets, git credentials, run event signing secrets, run and schedule arguments and submitted run environments, and local requester inputs, plus
// signing keys, under the cipher's current key. Each step commits on its own and repeats safely.
// Verify only counts, reporting how many rows still open under the previous key alone.
func (o *Operator) RotateSealedValues(ctx context.Context, a capability.Authorized, cipher SealedRotationCipher, verify bool) (SealedRotation, error) {
	return o.rotateSealedValues(ctx, &a, cipher, verify, false)
}

// SealPlaintextValues seals legacy argument documents without rotating existing envelopes.
func (o *Operator) SealPlaintextValues(ctx context.Context, cipher SealedRotationCipher) (SealedRotation, error) {
	return o.rotateSealedValues(ctx, nil, cipher, false, true)
}

func (o *Operator) rotateSealedValues(ctx context.Context, a *capability.Authorized, cipher SealedRotationCipher, verify, plaintextOnly bool) (SealedRotation, error) {
	if cipher == nil {
		return SealedRotation{}, authorityError(hosterr.Unavailable, "bound secret encryption is unavailable")
	}
	// safety: lazy key creation probes the store and cannot borrow a connection held by rotation.
	if lazy, ok := cipher.(interface {
		PrepareSealing(context.Context) (SealedRotationCipher, error)
	}); ok && !verify {
		prepared, err := lazy.PrepareSealing(ctx)
		if err != nil {
			return SealedRotation{}, err
		}
		cipher = prepared
	}

	total := SealedRotation{Unreadable: []SealedRowName{}}
	step := func(fn func(*sealedRotationTx) error, teams ...Team) error {
		var done SealedRotation
		err := o.s.inTx(ctx, func(tx *storeTx) error {
			if err := acquireLockSet(ctx, tx, func() lockSet {
				set := lockSet{}
				if a != nil {
					set = discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority})
				}
				for _, team := range teams {
					set.add(lockTeam, true, string(team))
				}
				return set
			}); err != nil {
				return err
			}
			if a != nil {
				if err := sealedRotationAuthorityTx(ctx, tx, *a); err != nil {
					return err
				}
			}
			r := &sealedRotationTx{tx: tx, cipher: cipher, verify: verify, plaintextOnly: plaintextOnly}
			if err := fn(r); err != nil {
				return err
			}
			done = r.out
			return nil
		})
		if err != nil {
			return err
		}
		total.AlreadySealed += done.AlreadySealed
		total.Secrets += done.Secrets
		total.GitCredentials += done.GitCredentials
		total.RunEnvironments += done.RunEnvironments
		total.RunArguments += done.RunArguments
		total.PlaintextRunArguments += done.PlaintextRunArguments
		total.Settings += done.Settings
		total.RunEventSubscriptions += done.RunEventSubscriptions
		total.PreviousKeyOnly += done.PreviousKeyOnly
		total.Unreadable = append(total.Unreadable, done.Unreadable...)
		return nil
	}
	var teams []Team
	if err := step(func(r *sealedRotationTx) (err error) {
		teams, err = rotationTeamsTx(ctx, r.tx)
		return err
	}); err != nil {
		return total, err
	}
	for _, team := range teams {
		if err := step(func(r *sealedRotationTx) error { return r.tenantCredentials(ctx, &Tenant{s: o.s, team: team}) }, team); err != nil {
			return total, err
		}
		if err := step(func(r *sealedRotationTx) error { return r.runEventSubscriptions(ctx, team) }, team); err != nil {
			return total, err
		}
		for _, fires := range []bool{false, true} {
			for after, more := "", true; more; {
				if err := step(func(r *sealedRotationTx) (err error) {
					after, more, err = r.scheduleArguments(ctx, team, after, fires)
					return err
				}, team); err != nil {
					return total, err
				}
			}
		}
		for after, more := "", true; more; {
			if err := step(func(r *sealedRotationTx) (err error) {
				after, more, err = r.runArguments(ctx, team, after)
				return err
			}); err != nil {
				return total, err
			}
		}
		for _, local := range []bool{false, true} {
			for after, more := "", true; more; {
				if err := step(func(r *sealedRotationTx) (err error) {
					after, more, err = r.runEnvironments(ctx, team, after, local)
					return err
				}); err != nil {
					return total, err
				}
			}
		}
	}
	for _, key := range []string{metaKeySessionCSRFKey, metaKeyLocalObjects} {
		if err := step(func(r *sealedRotationTx) error { return r.settingKey(ctx, key) }); err != nil {
			return total, err
		}
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
	rows, err := tx.queryContext(ctx, `SELECT name FROM teams ORDER BY name`)
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
		value, err := r.reseal(secrets.PurposeSecret, t.team, row.Name, row.SealScope(), row.Shared, row.Masked, row.Value)
		if errors.Is(err, errSealedUnreadable) || errors.Is(err, errSealedUnchanged) {
			continue
		}
		if err != nil {
			return err
		}
		r.out.Secrets++
		if r.verify {
			continue
		}
		if _, err := r.tx.execContext(ctx, `UPDATE secrets SET value=? WHERE team=? AND name=? AND repo=? AND pipeline=?`, value, t.team, row.Name, row.Repo, row.Pipeline); err != nil {
			return err
		}
	}
	credentials, err := t.credentialGitTx(ctx, r.tx)
	if err != nil {
		return err
	}
	for _, row := range credentials {
		value, err := r.reseal(secrets.PurposeGitCredential, t.team, "git-credential:"+row.Host, row.CredentialScope(), false, true, row.Secret)
		if errors.Is(err, errSealedUnreadable) || errors.Is(err, errSealedUnchanged) {
			continue
		}
		if err != nil {
			return err
		}
		r.out.GitCredentials++
		if r.verify {
			continue
		}
		if _, err := r.tx.execContext(ctx, `UPDATE git_credentials SET secret=? WHERE team=? AND id=?`, value, t.team, row.ID); err != nil {
			return err
		}
	}
	return nil
}

func (r *sealedRotationTx) runEnvironments(ctx context.Context, team Team, after string, local bool) (string, bool, error) {
	query := `SELECT id,submitted_env FROM runs WHERE team=? AND submitted_env<>'' AND id>? ORDER BY id LIMIT ?`
	update := `UPDATE runs SET submitted_env=? WHERE team=? AND id=?`
	scope := "run-environment"
	if local {
		query = `SELECT run_id,envelope FROM local_run_secrets WHERE team=? AND run_id>? ORDER BY run_id LIMIT ?`
		update = `UPDATE local_run_secrets SET envelope=? WHERE team=? AND run_id=?`
		scope = "local-secrets"
	}
	rows, err := r.tx.queryContext(ctx, query+r.tx.forUpdate(), team, after, sealedRotationRunPage)
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
		value, err := r.reseal(secrets.PurposeRunEnvironment, team, e.run, scope, false, true, e.value)
		if errors.Is(err, errSealedUnreadable) || errors.Is(err, errSealedUnchanged) {
			continue
		}
		if err != nil {
			return after, false, err
		}
		if local {
			r.out.Secrets++
		} else {
			r.out.RunEnvironments++
		}
		if r.verify {
			continue
		}
		if _, err := r.tx.execContext(ctx, update, value, team, e.run); err != nil {
			return after, false, err
		}
	}
	if len(page) == 0 {
		return after, false, nil
	}
	return page[len(page)-1].run, len(page) == sealedRotationRunPage, nil
}

func (r *sealedRotationTx) settingKey(ctx context.Context, key string) error {
	var envelope []byte
	err := r.tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=? AND sealed=1`+r.tx.forUpdate(), key).Scan(&envelope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	value, err := r.reseal(secrets.PurposeSetting, "", key, "settings", false, true, string(envelope))
	if errors.Is(err, errSealedUnreadable) || errors.Is(err, errSealedUnchanged) {
		return nil
	}
	if err != nil {
		return err
	}
	r.out.Settings++
	if r.verify {
		return nil
	}
	_, err = r.tx.execContext(ctx, `UPDATE settings SET value=? WHERE key=? AND sealed=1`, []byte(value), key)
	return err
}

// safety: a page also seals documents stored before argument sealing, so rotation retires their plaintext.
func (r *sealedRotationTx) runArguments(ctx context.Context, team Team, after string) (string, bool, error) {
	rows, err := r.tx.queryContext(ctx, `SELECT id,request_json,spec_json,plan_json FROM runs WHERE team=? AND id>? ORDER BY id LIMIT ?`+r.tx.forUpdate(), team, after, sealedRotationRunPage)
	if err != nil {
		return after, false, err
	}
	type document struct {
		run                 string
		request, spec, plan []byte
	}
	var page []document
	for rows.Next() {
		var d document
		if err := rows.Scan(&d.run, &d.request, &d.spec, &d.plan); err != nil {
			return after, false, errors.Join(err, rows.Close())
		}
		page = append(page, d)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return after, false, err
	}
	for _, d := range page {
		for _, column := range []struct {
			name string
			raw  []byte
			next func() ([]byte, error)
		}{
			{"request_json", d.request, func() ([]byte, error) {
				return rotateRunDocument(r, team, d.run, requestArgumentsScope, d.request, requestArgumentSlots)
			}},
			{"spec_json", d.spec, func() ([]byte, error) {
				return rotateRunDocument(r, team, d.run, specArgumentsScope, d.spec, specArgumentSlots)
			}},
			{"plan_json", d.plan, func() ([]byte, error) {
				return r.argumentBody(team, d.run, planArgumentsScope, d.plan, true)
			}},
		} {
			value, err := column.next()
			if (errors.Is(err, errSealedUnreadable) || errors.Is(err, errSealedUnchanged)) || err == nil && value == nil {
				continue
			}
			if err != nil {
				return after, false, err
			}
			if _, err := r.tx.execContext(ctx, `UPDATE runs SET `+column.name+`=? WHERE team=? AND id=?`, value, team, d.run); err != nil {
				return after, false, err
			}
		}
		if err := r.expansionArguments(ctx, team, d.run); err != nil {
			return after, false, err
		}
	}
	if len(page) == 0 {
		return after, false, nil
	}
	return page[len(page)-1].run, len(page) == sealedRotationRunPage, nil
}

func (r *sealedRotationTx) expansionArguments(ctx context.Context, team Team, run string) error {
	rows, err := r.tx.queryContext(ctx, `SELECT node_id,spec_json,expansion_body,expansion_registration FROM nodes WHERE team=? AND run_id=? ORDER BY node_id`+r.tx.forUpdate(), team, run)
	if err != nil {
		return err
	}
	type registration struct {
		node            string
		raw, spec, body []byte
	}
	var found []registration
	for rows.Next() {
		var e registration
		if err := rows.Scan(&e.node, &e.spec, &e.body, &e.raw); err != nil {
			return errors.Join(err, rows.Close())
		}
		found = append(found, e)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, e := range found {
		for _, column := range []struct {
			name, scope string
			raw         []byte
		}{
			{"spec_json", nodeArgumentsScope(e.node), e.spec},
			{"expansion_body", expansionBodyArgumentsScope(e.node), e.body},
		} {
			var value []byte
			var err error
			if column.name == "spec_json" {
				value, err = r.nodeArguments(team, run, e.node, column.raw)
			} else {
				value, err = r.argumentBody(team, run, column.scope, column.raw, true)
			}
			if (errors.Is(err, errSealedUnreadable) || errors.Is(err, errSealedUnchanged)) || err == nil && value == nil {
				continue
			}
			if err != nil {
				return err
			}
			if _, err := r.tx.execContext(ctx, `UPDATE nodes SET `+column.name+`=? WHERE team=? AND run_id=? AND node_id=?`, value, team, run, e.node); err != nil {
				return err
			}
		}
		value, err := rotateRunDocument(r, team, run, expansionArgumentsScope(e.node), e.raw, registrationArgumentSlots)
		if (errors.Is(err, errSealedUnreadable) || errors.Is(err, errSealedUnchanged)) || err == nil && value == nil {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := r.tx.execContext(ctx, `UPDATE nodes SET expansion_registration=? WHERE team=? AND run_id=? AND node_id=?`, value, team, run, e.node); err != nil {
			return err
		}
	}
	return nil
}

// safety: a nil replacement leaves raw as stored, which verify relies on.
func rotateRunDocument[T any](r *sealedRotationTx, team Team, run, scope string, raw []byte, slots func(*T) argumentSlots) ([]byte, error) {
	if err := r.checkArgumentJSON(team, run, raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	envelope, err := sealedArgumentsEnvelope(raw)
	if err != nil {
		return nil, r.unreadableArgument(team, run)
	}
	if envelope != "" {
		value, err := r.reseal(secrets.PurposeRunArguments, team, run, scope, false, true, envelope)
		if err != nil {
			return nil, err
		}
		r.out.RunArguments++
		if r.verify {
			return nil, nil
		}
		return replaceSealedArguments(raw, value)
	}
	var doc T
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, r.unreadableArgument(team, run)
	}
	stored, sealed, err := encodeRunDocument(r.cipher, team, run, scope, doc, slots)
	if err != nil || !sealed {
		return nil, err
	}
	r.out.PlaintextRunArguments++
	if r.verify {
		return nil, nil
	}
	return stored, nil
}

func (r *sealedRotationTx) checkArgumentJSON(team Team, name string, raw []byte) error {
	if len(raw) != 0 && !json.Valid(raw) {
		r.out.Unreadable = append(r.out.Unreadable, SealedRowName{Purpose: secrets.PurposeRunArguments, Team: team, Name: name})
		return errSealedUnreadable
	}
	return nil
}

func (r *sealedRotationTx) unreadableArgument(team Team, name string) error {
	r.out.Unreadable = append(r.out.Unreadable, SealedRowName{Purpose: secrets.PurposeRunArguments, Team: team, Name: name})
	return errSealedUnreadable
}
