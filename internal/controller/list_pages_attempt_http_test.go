package controller

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestJobAttemptHTTPPagesKeepNumericOrder(t *testing.T) {
	s, session := pauseHTTPFixture(t)
	options, _ := selectorClientOptions(t, s, session, `{"grants":["runs.read"]}`)
	raw, err := options.Bearer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `UPDATE nodes SET generation=12,attempt=12 WHERE team='a' AND run_id='run' AND node_id='job'`)
	for attempt := 12; attempt > 0; attempt-- {
		engineSQL(t, s.store, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome) VALUES ('a','run','job',?,?,'holder','prefix','runner','runner','local',?,?,'failed')`, attempt, attempt, attempt, attempt+1)
	}
	cursor, expected := "", int64(1)
	for range 4 {
		w := engineClaimHTTP(t, s.Handler(), raw, http.MethodGet, "/api/v1/runs/run/jobs/job/attempts?limit=3&cursor="+url.QueryEscape(cursor), "", 200)
		var page struct {
			Attempts   []controllerwire.AttemptDetail `json:"attempts"`
			NextCursor string                         `json:"next_cursor"`
		}
		if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Attempts) != 3 {
			t.Fatalf("attempts: %s", w.Body)
		}
		for _, attempt := range page.Attempts {
			if attempt.Attempt != expected {
				t.Fatalf("attempt=%d want %d", attempt.Attempt, expected)
			}
			expected++
		}
		cursor = page.NextCursor
	}
	if expected != 13 || cursor != "" {
		t.Fatalf("incomplete attempts: %d %q", expected, cursor)
	}
}
