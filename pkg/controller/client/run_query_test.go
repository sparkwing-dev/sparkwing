package client

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRunQueryFiltersReachController(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		for key, want := range map[string]string{"pipeline": "build", "status": "!success", "sha": "abc123", "error": "permission denied", "search": "deploy -test", "root_only": "true", "after_number": "42", "started_after": now.Format(time.RFC3339Nano), "started_before": now.Format(time.RFC3339Nano), "finished_after": now.Format(time.RFC3339Nano), "finished_before": now.Format(time.RFC3339Nano)} {
			if got := q.Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		if !reflect.DeepEqual(q["branch"], []string{"main", "!release"}) || !reflect.DeepEqual(q["repo"], []string{"acme/api", "acme/web"}) {
			t.Errorf("lost repeated filters: %v", q)
		}
		return engineResponse(200, `{"runs":[],"next_cursor":"","truncated":false}`), nil
	})
	_, err := c.ListRuns(t.Context(), controllerwire.RunQuery{Limit: 20, Pipeline: "build", Status: "!success", SHA: "abc123", Error: "permission denied", Search: "deploy -test", RootOnly: true, AfterNumber: 42, Branches: []string{"main", "!release"}, Repos: []string{"acme/api", "acme/web"}, StartedAfter: now, StartedBefore: now, FinishedAfter: now, FinishedBefore: now})
	if err != nil {
		t.Fatal(err)
	}
}
