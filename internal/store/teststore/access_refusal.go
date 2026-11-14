package teststore

import (
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// AccessRefusalRow is one access.refused audit row as stored.
type AccessRefusalRow struct {
	Team, Actor, Subject, RunID, Account string
	Attrs                                map[string]any
	// Raw is every stored column of the row joined, for scanning it for leaked values.
	Raw string
}

// AccessRefusals reads every access.refused row, oldest first.
func AccessRefusals(t T, st *store.Store) []AccessRefusalRow {
	t.Helper()
	rows, err := SQL(t, st).QueryContext(t.Context(), `SELECT id,COALESCE(team,''),account,kind,subject_id,actor,attrs,run_id FROM audit_events WHERE kind='access.refused' ORDER BY ts,id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	var out []AccessRefusalRow
	for rows.Next() {
		var row AccessRefusalRow
		var id, kind, attrs string
		if err := rows.Scan(&id, &row.Team, &row.Account, &kind, &row.Subject, &row.Actor, &attrs, &row.RunID); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(attrs), &row.Attrs); err != nil {
			t.Fatal(err)
		}
		row.Raw = id + "\x00" + row.Team + "\x00" + row.Account + "\x00" + kind + "\x00" + row.Subject + "\x00" + row.Actor + "\x00" + attrs + "\x00" + row.RunID
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
