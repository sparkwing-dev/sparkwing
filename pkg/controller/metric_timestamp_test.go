package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestMetricTimestamp(t *testing.T) {
	for _, tc := range []struct {
		name, timestamp string
		status          int
	}{
		{"malformed", "not-a-time", http.StatusBadRequest},
		{"explicit", "2026-01-01T00:00:00Z", http.StatusNoContent},
		{"empty", "", http.StatusNoContent},
		{"omitted", "", http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			ctx := t.Context()
			if err := st.CreateRun(ctx, store.Run{ID: "run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(ctx, store.Node{RunID: "run", NodeID: "node", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			handler := (&Server{store: st}).handleAddNodeMetric
			body := `{"cpu_millicores":1,"memory_bytes":2}`
			if tc.name != "omitted" {
				body = `{"ts":"` + tc.timestamp + `","cpu_millicores":1,"memory_bytes":2}`
			}
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.SetPathValue("id", "run")
			req.SetPathValue("nodeID", "node")
			response := httptest.NewRecorder()
			before := time.Now()
			handler(response, req)
			after := time.Now()
			if response.Code != tc.status {
				t.Errorf("status = %d, want %d; %s", response.Code, tc.status, response.Body)
			}
			rows, err := teststore.ForTeam(t, st, store.DefaultTeam).ListNodeMetrics(ctx, "run", "node")
			if err != nil {
				t.Fatal(err)
			}
			if tc.status == http.StatusBadRequest {
				if len(rows) != 0 {
					t.Errorf("malformed timestamp persisted: %+v", rows)
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("samples = %d", len(rows))
			}
			if tc.timestamp == "" {
				if rows[0].TS.Before(before) || rows[0].TS.After(after) {
					t.Errorf("receipt time = %v, outside request interval", rows[0].TS)
				}
			} else if !rows[0].TS.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
				t.Errorf("timestamp changed: %v", rows[0].TS)
			}
		})
	}
}
