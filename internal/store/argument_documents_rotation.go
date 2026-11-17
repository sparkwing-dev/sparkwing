package store

import (
	"context"
	"errors"
)

func (r *sealedRotationTx) argumentBody(team Team, name, scope string, raw []byte, job bool) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	envelope, err := sealedArgumentsEnvelope(raw)
	if err != nil {
		return nil, corruptRunArguments()
	}
	if envelope != "" {
		return rotateRunDocument(r, team, name, scope, raw, bodyArgumentSlots)
	}
	if job && !hasJobArgumentDefaults(raw) {
		return nil, nil
	}
	stored, err := encodeArgumentBody(r.cipher, team, name, scope, raw)
	if err != nil {
		return nil, err
	}
	r.out.PlaintextRunArguments++
	if r.verify {
		return nil, nil
	}
	return stored, nil
}

func (r *sealedRotationTx) scheduleArguments(ctx context.Context, team Team, after string, fires bool) (string, bool, error) {
	query := `SELECT id,args,override_args,override_base,consent_json FROM cron_schedules WHERE team=? AND id>? ORDER BY id LIMIT ?`
	if fires {
		query = `SELECT id,schedule_id,args FROM cron_fires WHERE team=? AND id>? ORDER BY id LIMIT ?`
	}
	rows, err := r.tx.queryContext(ctx, query+r.tx.forUpdate(), team, after, sealedRotationRunPage)
	if err != nil {
		return after, false, err
	}
	type document struct {
		id, schedule                  string
		args, override, base, consent []byte
	}
	var page []document
	for rows.Next() {
		var d document
		if fires {
			err = rows.Scan(&d.id, &d.schedule, &d.args)
		} else {
			err = rows.Scan(&d.id, &d.args, &d.override, &d.base, &d.consent)
		}
		if err != nil {
			return after, false, errors.Join(err, rows.Close())
		}
		page = append(page, d)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return after, false, err
	}
	for _, d := range page {
		columns := []struct {
			name string
			raw  []byte
		}{{"args", d.args}, {"override_args", d.override}, {"override_base", d.base}, {"consent_json", d.consent}}
		if fires {
			columns = columns[:1]
		}
		for _, column := range columns {
			scope, table := scheduleArgumentsScope(column.name), "cron_schedules"
			if fires {
				scope, table = fireArgumentsScope(d.schedule), "cron_fires"
			}
			value, err := r.argumentBody(team, d.id, scope, column.raw, false)
			if errors.Is(err, errSealedUnreadable) || err == nil && value == nil {
				continue
			}
			if err != nil {
				return after, false, err
			}
			if _, err := r.tx.execContext(ctx, `UPDATE `+table+` SET `+column.name+`=? WHERE team=? AND id=?`, string(value), team, d.id); err != nil {
				return after, false, err
			}
		}
	}
	if len(page) == 0 {
		return after, false, nil
	}
	return page[len(page)-1].id, len(page) == sealedRotationRunPage, nil
}
