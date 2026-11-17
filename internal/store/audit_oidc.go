package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// OIDCAuditDigester computes keyed audience and subject digests using external
// deployment keys. An empty keyID chooses the current key; a named key must be retained.
type OIDCAuditDigester interface {
	OIDCAuditDigest(keyID, field, value string) (string, string, error)
}

func (s *Store) oidcAuditDigest(keyID, field, value string) (string, auditDigest, error) {
	if s.auditDigests == nil {
		return "", auditDigest{}, errors.New("OIDC audit digest key is unavailable")
	}
	id, encoded, err := s.auditDigests.OIDCAuditDigest(keyID, field, value)
	if err != nil {
		return "", auditDigest{}, err
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil || len(raw) != 32 || len(id) != 64 {
		return "", auditDigest{}, errors.New("invalid OIDC audit digest")
	}
	var digest auditDigest
	copy(digest.sum[:], raw)
	return id, digest, nil
}

func (op *Operator) reconcileOIDCAudit(ctx context.Context) error {
	s := op.s
	var done int
	if err := s.queryRow(ctx, `SELECT completed FROM audit_privacy_migrations WHERE version=1`).Scan(&done); err != nil {
		return err
	}
	if done == 1 {
		return op.checkOIDCAuditPrivacy(ctx)
	}
	var haveRows bool
	if err := s.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_events WHERE kind='oidc.minted')`).Scan(&haveRows); err != nil {
		return err
	}
	var keyID string
	if haveRows {
		// safety: lazy local key creation queries the store, so resolve it before holding the write transaction.
		var err error
		keyID, _, err = s.oidcAuditDigest("", "audience", "")
		if err != nil {
			return err
		}
	}
	err := s.inTx(ctx, func(tx *storeTx) error {
		if tx.dialect == DialectPostgres {
			if _, err := tx.execContext(ctx, migrateAdvisoryLock); err != nil {
				return err
			}
		}
		var completed int
		if err := tx.QueryRowContext(ctx, `SELECT completed FROM audit_privacy_migrations WHERE version=1`).Scan(&completed); err != nil {
			return err
		}
		if completed == 1 {
			return nil
		}
		version, err := recordedSchemaVersion(ctx, tx)
		if err != nil {
			return err
		}
		if version != 40 {
			return errors.New("OIDC audit scrub requires schema 40")
		}
		if err := verifySchemaBaseline(ctx, tx, ""); err != nil {
			return err
		}
		if tx.dialect == DialectPostgres {
			if _, err := tx.execContext(ctx, `LOCK TABLE audit_events IN ACCESS EXCLUSIVE MODE`); err != nil {
				return err
			}
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM audit_events WHERE kind='oidc.minted')`).Scan(&haveRows); err != nil {
			return err
		}
		var count int64
		if haveRows {
			if keyID == "" {
				return errors.New("OIDC audit privacy migration requires the external secrets keyring when OIDC history exists")
			}
			count, err = op.prepareOIDCScrub(ctx, tx, keyID)
			if err != nil {
				return err
			}
			if err := replaceAuditUpdateGuard(ctx, tx, oidcScrubAllowance(tx.dialect)); err != nil {
				return err
			}
			if _, err := tx.execContext(ctx, `UPDATE audit_events SET attrs=(SELECT new_attrs FROM oidc_audit_scrub_rows WHERE id=audit_events.id) WHERE id IN (SELECT id FROM oidc_audit_scrub_rows)`); err != nil {
				return fmt.Errorf("scrub OIDC audit: %w", err)
			}
			if err := replaceAuditUpdateGuard(ctx, tx, ""); err != nil {
				return err
			}
			if _, err := tx.execContext(ctx, `DROP TABLE oidc_audit_scrub_rows`); err != nil {
				return err
			}
		}
		if err := tx.operator.recordAudit(ctx, tx, auditContext{Actor: auditActor{system: auditController}}, auditOIDCScrub{Rows: count, KeyID: keyID, Version: 1}); err != nil {
			return err
		}
		_, err = tx.execContext(ctx, `UPDATE audit_privacy_migrations SET completed=1 WHERE version=1 AND completed=0`)
		return err
	})
	if err != nil {
		return err
	}
	return op.checkOIDCAuditPrivacy(ctx)
}

func (op *Operator) checkOIDCAuditPrivacy(ctx context.Context) error {
	fields := `json_type(attrs,'$.audience') IS NOT NULL OR json_type(attrs,'$.subject') IS NOT NULL`
	if op.s.dialect == DialectPostgres {
		fields = `jsonb_typeof(CAST(attrs AS JSONB))='object' AND (jsonb_exists(CAST(attrs AS JSONB),'audience') OR jsonb_exists(CAST(attrs AS JSONB),'subject'))`
	}
	var raw bool
	if err := op.s.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_events WHERE kind='oidc.minted' AND (`+fields+`))`).Scan(&raw); err != nil {
		return errors.New("OIDC audit privacy check failed; investigate the retained audit payloads before startup")
	}
	if raw {
		return errors.New("raw OIDC audit identity exists after privacy reconciliation; stop older controllers and investigate before startup")
	}
	return nil
}

func (op *Operator) prepareOIDCScrub(ctx context.Context, tx *storeTx, keyID string) (int64, error) {
	s := op.s
	if _, err := tx.execContext(ctx, `CREATE TABLE oidc_audit_scrub_rows(id TEXT NOT NULL PRIMARY KEY,old_attrs TEXT NOT NULL,new_attrs TEXT NOT NULL)`); err != nil {
		return 0, err
	}
	// safety: bounded pages avoid retaining the audit horizon in process memory; the migration transaction owns the rows.
	var count int64
	after := ""
	for {
		rows, err := tx.queryContext(ctx, `SELECT id,attrs FROM audit_events WHERE kind='oidc.minted' AND (?=0 OR id>?) ORDER BY id LIMIT 256`, boolInt(count > 0), after)
		if err != nil {
			return 0, err
		}
		type replacement struct{ id, old, updated string }
		var page []replacement
		for rows.Next() {
			var row replacement
			if err := rows.Scan(&row.id, &row.old); err != nil {
				return 0, errors.Join(err, rows.Close())
			}
			row.updated, err = s.scrubOIDCAttrs(keyID, row.old)
			if err != nil {
				return 0, errors.Join(errors.New("OIDC audit privacy migration refused a malformed historical payload"), rows.Close())
			}
			page = append(page, row)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return 0, err
		}
		if len(page) == 0 {
			return count, nil
		}
		for _, row := range page {
			if _, err := tx.execContext(ctx, `INSERT INTO oidc_audit_scrub_rows(id,old_attrs,new_attrs) VALUES (?,?,?)`, row.id, row.old, row.updated); err != nil {
				return 0, err
			}
			count++
			after = row.id
		}
	}
}

func (s *Store) scrubOIDCAttrs(keyID, body string) (string, error) {
	var attrs map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &attrs); err != nil || len(attrs) != 5 {
		return "", ErrInvalidInput
	}
	for _, field := range []string{"node_digest", "audience", "token_id", "subject", "expires_at"} {
		if _, ok := attrs[field]; !ok {
			return "", ErrInvalidInput
		}
	}
	for _, field := range []string{"audience", "subject"} {
		var value string
		if err := json.Unmarshal(attrs[field], &value); err != nil || value == "" {
			return "", ErrInvalidInput
		}
		_, digest, err := s.oidcAuditDigest(keyID, field, value)
		if err != nil {
			return "", err
		}
		encoded, err := json.Marshal(digest)
		if err != nil {
			return "", err
		}
		delete(attrs, field)
		attrs[field+"_digest"] = encoded
	}
	encoded, err := json.Marshal(keyID)
	if err != nil {
		return "", err
	}
	attrs["digest_key_id"] = encoded
	result, err := json.Marshal(attrs)
	return string(result), err
}

func oidcScrubAllowance(d Dialect) string {
	equal := " IS "
	if d == DialectPostgres {
		equal = " IS NOT DISTINCT FROM "
	}
	conditions := []string{
		`(SELECT version FROM sparkwing_schema_version)=40`,
		`(SELECT baseline_digest FROM sparkwing_schema_version)='` + schemaBaselineDigest() + `'`,
		`(SELECT completed FROM audit_privacy_migrations WHERE version=1)=0`,
		`OLD.kind='oidc.minted'`,
		`EXISTS(SELECT 1 FROM oidc_audit_scrub_rows m WHERE m.id=OLD.id AND m.old_attrs=OLD.attrs AND m.new_attrs=NEW.attrs)`,
	}
	for _, column := range []string{"id", "recorded_at", "ts", "team", "run_id", "account", "kind", "subject_id", "actor", "actor_is_account", "subject_is_account"} {
		conditions = append(conditions, "NEW."+column+equal+"OLD."+column)
	}
	return strings.Join(conditions, " AND ")
}

func replaceAuditUpdateGuard(ctx context.Context, tx *storeTx, allow string) error {
	if tx.dialect == DialectPostgres {
		permit := ""
		if allow != "" {
			permit = "IF TG_OP = 'UPDATE' THEN IF " + allow + " THEN RETURN NEW; END IF; END IF;"
		}
		_, err := tx.execContext(ctx, `CREATE OR REPLACE FUNCTION reject_audit_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 `+permit+`
 IF TG_OP = 'DELETE' THEN
  IF OLD.recorded_at < `+auditRetentionCutoffSQL(tx.dialect)+` THEN RETURN OLD; END IF;
 END IF;
 RAISE EXCEPTION 'audit events are append-only within retention'; END $$`)
		return err
	}
	if _, err := tx.execContext(ctx, `DROP TRIGGER IF EXISTS audit_events_no_UPDATE`); err != nil {
		return err
	}
	when := ""
	if allow != "" {
		when = " WHEN NOT COALESCE((" + allow + "),0)"
	}
	_, err := tx.execContext(ctx, `CREATE TRIGGER audit_events_no_UPDATE BEFORE UPDATE ON audit_events`+when+` BEGIN SELECT RAISE(ABORT, 'audit events are append-only'); END`)
	return err
}
