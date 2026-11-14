package teststore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runevents"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// SeedRunEventSubscription inserts a subscription row without the audited
// administration path. secret is stored as given, so callers that send pass
// an envelope from store.SealRunEventSubscription.
func SeedRunEventSubscription(ctx context.Context, st *store.Store, team store.Team, id, url, secret string, filter runevents.Filter) error {
	lists := make([]string, 4)
	for i, values := range [][]string{filter.EventTypes, filter.Statuses, filter.Pipelines, filter.Repositories} {
		if values == nil {
			values = []string{}
		}
		raw, err := json.Marshal(values)
		if err != nil {
			return err
		}
		lists[i] = string(raw)
	}
	at := Now(st).UnixNano()
	_, err := Exec(ctx, st, `INSERT INTO run_event_subscriptions(team,id,url,event_types,statuses,pipelines,repositories,secret,description,created_principal,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,'','fixture',?,?)`, team, id, url, lists[0], lists[1], lists[2], lists[3], secret, at, at)
	return err
}

// RunEventDeliveryRow is one stored delivery as a test reads it back.
type RunEventDeliveryRow struct {
	SubscriptionID, EventID, RunID, EventType, State, LastError string
	Body                                                        []byte
	Attempts                                                    int64
	NextAt, DeadlineAt                                          time.Time
	LastStatus                                                  sql.NullInt64
	Finished                                                    bool
}

// SeedRunEventDelivery inserts a pending delivery due at next with a deadline
// one delivery window after it.
func SeedRunEventDelivery(ctx context.Context, st *store.Store, team store.Team, subscription, eventID, runID string, body []byte, next time.Time) error {
	_, err := Exec(ctx, st, `INSERT INTO run_event_deliveries(team,subscription_id,event_id,run_id,event_type,body,state,attempts,next_at,deadline_at,created_at) VALUES (?,?,?,?,?,?,'pending',0,?,?,?)`, team, subscription, eventID, runID, runevents.EventFinished, body, next.UnixNano(), next.Add(store.RunEventDeliveryWindow).UnixNano(), next.UnixNano())
	return err
}

// RunEventDeliveries returns a team's deliveries ordered by subscription and event.
func RunEventDeliveries(ctx context.Context, st *store.Store, team store.Team) (_ []RunEventDeliveryRow, err error) {
	db, err := registeredDB(st)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, rebind(st, `SELECT subscription_id,event_id,run_id,event_type,state,last_error,body,attempts,next_at,deadline_at,last_status,finished_at FROM run_event_deliveries WHERE team=? ORDER BY subscription_id,event_id`), team)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var out []RunEventDeliveryRow
	for rows.Next() {
		var r RunEventDeliveryRow
		var next, deadline int64
		var finished sql.NullInt64
		if err := rows.Scan(&r.SubscriptionID, &r.EventID, &r.RunID, &r.EventType, &r.State, &r.LastError, &r.Body, &r.Attempts, &next, &deadline, &r.LastStatus, &finished); err != nil {
			return nil, err
		}
		r.NextAt, r.DeadlineAt, r.Finished = time.Unix(0, next), time.Unix(0, deadline), finished.Valid
		out = append(out, r)
	}
	return out, rows.Err()
}
