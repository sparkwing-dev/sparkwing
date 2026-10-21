package store

import "context"

func (op *Operator) installAuditProtection(ctx context.Context, tx *storeTx) error {
	if err := op.installRunTombstones(ctx, tx); err != nil {
		return err
	}
	if tx.dialect == DialectPostgres {
		_, err := tx.ExecContext(ctx, `CREATE FUNCTION reject_audit_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit events are append-only'; END $$`)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `CREATE TRIGGER audit_events_append_only BEFORE UPDATE OR DELETE OR TRUNCATE ON audit_events FOR EACH STATEMENT EXECUTE FUNCTION reject_audit_mutation()`)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `REVOKE UPDATE, DELETE, TRUNCATE ON audit_events FROM PUBLIC, CURRENT_USER`)
		return err
	}
	// SQLite REPLACE skips delete triggers unless recursive triggers are enabled.
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER audit_events_no_replace BEFORE INSERT ON audit_events
        WHEN EXISTS(SELECT 1 FROM audit_events WHERE id=NEW.id)
        BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		return err
	}
	for _, action := range []string{"UPDATE", "DELETE"} {
		if _, err := tx.ExecContext(ctx, `CREATE TRIGGER audit_events_no_`+action+` BEFORE `+action+` ON audit_events BEGIN SELECT RAISE(ABORT, 'audit events are append-only'); END`); err != nil {
			return err
		}
	}
	return nil
}

func (op *Operator) installRunTombstones(ctx context.Context, tx *storeTx) error {
	identity := "'run-deleted:' || length(OLD.team) || ':' || OLD.team || ':' || OLD.id"
	if tx.dialect == DialectPostgres {
		_, err := tx.ExecContext(ctx, `CREATE FUNCTION record_run_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
            INSERT INTO audit_events(id,ts,team,kind,subject_id,run_id) VALUES (`+identity+`,(extract(epoch FROM clock_timestamp())*1000000000)::bigint,OLD.team,'run.deleted',OLD.id,OLD.id) ON CONFLICT DO NOTHING;
            RETURN OLD; END $$`)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `CREATE TRIGGER runs_tombstone AFTER DELETE ON runs FOR EACH ROW EXECUTE FUNCTION record_run_tombstone()`)
		return err
	}
	_, err := tx.ExecContext(ctx, `CREATE TRIGGER runs_tombstone AFTER DELETE ON runs BEGIN
        INSERT INTO audit_events(id,ts,team,kind,subject_id,run_id) VALUES (`+identity+`,CAST(unixepoch('subsec')*1000000000 AS INTEGER),OLD.team,'run.deleted',OLD.id,OLD.id) ON CONFLICT DO NOTHING; END`)
	return err
}
