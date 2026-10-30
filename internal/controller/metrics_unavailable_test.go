package controller

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestMetricsHTTPCompletedCloudJobWithoutSamples(t *testing.T) {
	for _, tc := range []struct {
		name, status, outcome, query string
		locations                    []string
		samples, want                int
	}{
		{"completed-cloud", "done", "success", "", []string{"cloud", "cloud"}, 0, http.StatusServiceUnavailable},
		{"failed-cloud", "done", "failed", "", []string{"cloud", "cloud"}, 0, http.StatusServiceUnavailable},
		{"cloud-with-samples", "done", "success", "", []string{"cloud", "cloud"}, 1, http.StatusOK},
		{"cloud-page-exhausted", "done", "success", "?cursor=2026-01-01T00:00:00Z", []string{"cloud", "cloud"}, 1, http.StatusOK},
		{"running-cloud", "running", "", "", []string{"cloud"}, 0, http.StatusOK},
		{"pending-cloud", "pending", "", "", nil, 0, http.StatusOK},
		{"skipped-cloud", "done", "skipped", "", nil, 0, http.StatusOK},
		{"completed-local", "done", "success", "", []string{"local"}, 0, http.StatusOK},
		{"cloud-then-local", "done", "success", "", []string{"cloud", "local"}, 0, http.StatusOK},
		{"local-then-cloud", "done", "success", "", []string{"local", "cloud"}, 0, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, _, _ := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			var finish, holder, expiry any
			prefix := ""
			if tc.status == "done" {
				finish = start.Add(25 * time.Second).UnixNano()
			}
			if tc.status == "running" {
				holder, prefix, expiry = "holder", "prefix", start.Add(time.Hour).UnixNano()
			}
			location := "cloud"
			if len(tc.locations) > 0 {
				location = tc.locations[len(tc.locations)-1]
			}
			engineSQL(t, engine.Store, `UPDATE nodes SET status=?,outcome=?,attempt=?,generation=1,started_at=?,finished_at=?,claimed_by=?,capability_prefix=?,lease_expires_at=?,executor_kind='kubernetes',executor_location=? WHERE team='a' AND run_id='run' AND node_id='job'`, tc.status, tc.outcome, len(tc.locations), start.UnixNano(), finish, holder, prefix, expiry, location)
			for i, location := range tc.locations {
				var finish any
				outcome := ""
				if tc.status == "done" {
					finish, outcome = start.Add(12*time.Second).UnixNano(), tc.outcome
				}
				engineSQL(t, engine.Store, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome) VALUES ('a','run','job',?,1,'holder','prefix','kubernetes','cloud-runner',?,?,?,?)`, i+1, location, start.UnixNano(), finish, outcome)
				start = start.Add(13 * time.Second)
			}
			if tc.samples > 0 {
				engineSQL(t, engine.Store, `INSERT INTO node_metrics(team,run_id,node_id,ts,cpu_millicores,memory_bytes,cpu_time_nanos,kind) VALUES ('a','run','job',?,1000,4096,0,'interval')`, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
			}
			f, _ := metricHTTPFixture(t)
			f.s.store = engine.Store
			f.s.WithTenantResolver(func(ctx context.Context, team store.Team) (TenantHandle, error) {
				return engine.Store.ForTeam(ctx, team)
			})
			f.facts.snapshot.Authorities[0].Principal.Capability.Team = "a"
			f.s.WithEngineController(engine.EngineController)
			w := engineClaimHTTP(t, f.s.Handler(), f.raw, http.MethodGet, "/api/v1/runs/run/jobs/job/metrics"+tc.query, "", tc.want)
			if tc.want == http.StatusServiceUnavailable {
				if !strings.Contains(w.Body.String(), `"code":"unavailable"`) || !strings.Contains(w.Body.String(), "metrics unavailable on Cloud runners") {
					t.Fatalf("missing explicit Cloud metrics failure: %s", w.Body.String())
				}
			} else if tc.samples > 0 && tc.query == "" && !strings.Contains(w.Body.String(), `"cpu_millicores":1000`) {
				t.Fatalf("stored sample lost: %s", w.Body.String())
			}
		})
	}
}
