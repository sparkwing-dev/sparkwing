package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const defaultAuditRetentionDays = 400

func auditRecordingTimeSQL(d Dialect) string {
	if d == DialectPostgres {
		return "CAST(extract(epoch FROM statement_timestamp())*1000000000 AS BIGINT)"
	}
	return "CAST(unixepoch('subsec')*1000000000 AS INTEGER)"
}

// The guard and maintenance use the database clock and the same policy expression.
func auditRetentionCutoffSQL(d Dialect) string {
	value := "CAST(CAST(value AS TEXT) AS BIGINT)"
	if d == DialectPostgres {
		value = "CAST(convert_from(value, 'UTF8') AS BIGINT)"
	}
	return fmt.Sprintf(`(SELECT CASE WHEN days BETWEEN 1 AND %d THEN (%s)-days*%d ELSE NULL END
 FROM (SELECT COALESCE((SELECT %s FROM settings WHERE key='%s'),%d) AS days) policy)`,
		maxRetentionDays, strings.TrimPrefix(databaseTimeSQL(d), "SELECT "), int64(24*time.Hour), value, storageAuditRetentionKey, defaultAuditRetentionDays)
}

func (op *Operator) installAuditProtection(ctx context.Context, tx *storeTx) error {
	if err := op.installRunTombstones(ctx, tx); err != nil {
		return err
	}
	if tx.dialect == DialectPostgres {
		if _, err := tx.ExecContext(ctx, `CREATE FUNCTION stamp_audit_recording() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.recorded_at <> `+auditRecordingTimeSQL(tx.dialect)+` THEN RAISE EXCEPTION 'audit recording time is database assigned'; END IF;
 RETURN NEW; END $$`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `CREATE TRIGGER audit_events_recording_time BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION stamp_audit_recording()`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `CREATE FUNCTION reject_audit_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP = 'DELETE' THEN
  IF OLD.recorded_at < `+auditRetentionCutoffSQL(tx.dialect)+` THEN RETURN OLD; END IF;
 END IF;
 RAISE EXCEPTION 'audit events are append-only within retention'; END $$`)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `CREATE TRIGGER audit_events_append_only BEFORE UPDATE OR DELETE ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_audit_mutation()`)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `CREATE TRIGGER audit_events_no_truncate BEFORE TRUNCATE ON audit_events FOR EACH STATEMENT EXECUTE FUNCTION reject_audit_mutation()`)
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER audit_events_recording_time BEFORE INSERT ON audit_events
 WHEN NEW.recorded_at <> `+auditRecordingTimeSQL(tx.dialect)+`
 BEGIN SELECT RAISE(ABORT, 'audit recording time is database assigned'); END`); err != nil {
		return err
	}
	// SQLite REPLACE skips delete triggers unless recursive triggers are enabled.
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER audit_events_no_replace BEFORE INSERT ON audit_events
        WHEN EXISTS(SELECT 1 FROM audit_events WHERE id=NEW.id)
        BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER audit_events_no_UPDATE BEFORE UPDATE ON audit_events BEGIN SELECT RAISE(ABORT, 'audit events are append-only'); END`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER audit_events_no_DELETE BEFORE DELETE ON audit_events
 WHEN NOT COALESCE(OLD.recorded_at < `+auditRetentionCutoffSQL(tx.dialect)+`,0)
 BEGIN SELECT RAISE(ABORT, 'audit events are append-only within retention'); END`); err != nil {
		return err
	}
	return nil
}

func (op *Operator) installRunTombstones(ctx context.Context, tx *storeTx) error {
	if tx.dialect == DialectPostgres {
		_, err := tx.ExecContext(ctx, `CREATE FUNCTION record_run_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
            INSERT INTO audit_events(id,ts,team,kind,subject_id,run_id) VALUES ('event:' || gen_random_uuid()::text,(extract(epoch FROM clock_timestamp())*1000000000)::bigint,OLD.team,'run.deleted',OLD.id,OLD.id);
            RETURN OLD; END $$`)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `CREATE TRIGGER runs_tombstone AFTER DELETE ON runs FOR EACH ROW EXECUTE FUNCTION record_run_tombstone()`)
		return err
	}
	_, err := tx.ExecContext(ctx, `CREATE TRIGGER runs_tombstone AFTER DELETE ON runs BEGIN
        INSERT INTO audit_events(id,ts,team,kind,subject_id,run_id) VALUES ('event:' || lower(hex(randomblob(16))),CAST(unixepoch('subsec')*1000000000 AS INTEGER),OLD.team,'run.deleted',OLD.id,OLD.id); END`)
	return err
}
