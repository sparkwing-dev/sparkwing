package controller

import (
	"testing"
	"time"
)

func TestOperationalQueueSamplerReadsFinalStoreStates(t *testing.T) {
	restoreGlobals(t)
	engine, _ := engineRuntimeStore(t)
	s := engine.Store
	for _, id := range []string{"waiting", "ready", "held"} {
		engineSQL(t, s, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run',?,'pending',?,?)`, id, []byte(`{}`), []byte(`{}`))
	}
	engineSQL(t, s, `UPDATE nodes SET ready_at=1 WHERE node_id='ready'`)
	engineSQL(t, s, `UPDATE nodes SET claimed_by='runner',capability_prefix='claim',generation=1,lease_expires_at=? WHERE node_id='held'`, time.Now().Add(time.Hour).UnixNano())
	New(s, nil).sampleOperationalSeries(t.Context())
	body := scrapeRegistry(t)
	for series, want := range map[string]float64{
		`sparkwing_queue_depth{state="waiting"}`:          1,
		`sparkwing_queue_depth{state="ready"}`:            1,
		`sparkwing_queue_depth{state="claimed"}`:          1,
		`sparkwing_queue_depth{state="running"}`:          0,
		`sparkwing_queue_depth{state="approval_pending"}`: 0,
		`sparkwing_pending_nodes`:                         1,
		`sparkwing_active_runners`:                        1,
	} {
		if got := mustSample(t, body, series); got != want {
			t.Errorf("%s: %v; want %v", series, got, want)
		}
	}
}
