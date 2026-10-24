package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type metricWriterFake struct {
	RunTransactions
	team   store.Team
	seen   capability.Authorized
	sample store.MetricSample
	usage  store.NodeUsage
	calls  int
	err    error
}

func (f *metricWriterFake) Team() store.Team { return f.team }

func (f *metricWriterFake) ReadRun(_ context.Context, id string) (store.RunRow, error) {
	return store.RunRow{Team: f.team, Execution: runstate.Execution{ID: id}}, nil
}

func (f *metricWriterFake) WriteMetric(_ context.Context, a capability.Authorized, sample store.MetricSample) error {
	f.calls++
	f.seen, f.sample = a, sample
	return f.err
}

func (f *metricWriterFake) WriteUsage(_ context.Context, a capability.Authorized, usage store.NodeUsage) error {
	f.calls++
	f.seen, f.usage = a, usage
	return f.err
}

func metricHTTPFixture(t *testing.T) (scheduleHTTPFixture, *metricWriterFake) {
	t.Helper()
	f := newScheduleHTTPFixture(t)
	p := &f.facts.snapshot.Authorities[0].Principal
	p.Class = capability.CredentialBearer
	p.Capability.Subject.Kind = capability.SubjectMachine
	p.Capability.Issuance = capability.Award
	p.Capability.Purpose = capability.PurposeWork
	p.Capability.Unit, p.Capability.UnitID = capability.UnitNode, "unit"
	p.Capability.Grants = []capability.Grant{capability.NodeReport, capability.DataRead}
	p.Capability.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 1, Holder: "holder"}
	f.facts.snapshot.Binding = capability.BindingState{Current: p.Capability.Binding, LeaseExpiresAt: f.facts.snapshot.Now.Add(time.Minute)}
	f.s.WithEngineController(&EngineController{ResolveTenant: selectorTenant(store.DefaultTeam, nil)})
	writer := &metricWriterFake{team: store.DefaultTeam}
	testOnlyTenantPort[metricWriter](f.s, func(context.Context, store.Team) (metricWriter, error) { return writer, nil })
	return f, writer
}

func TestMetricTimestamp(t *testing.T) {
	for _, tc := range []struct {
		name, timestamp string
		status          int
	}{
		{"malformed", "not-a-time", 400},
		{"explicit", "2026-01-01T00:00:00Z", 204},
		{"empty", "", 204},
		{"omitted", "", 204},
		{"out-of-range", "9999-01-01T00:00:00Z", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, writer := metricHTTPFixture(t)
			body := `{"cpu_millicores":1,"memory_bytes":2}`
			if tc.name != "omitted" {
				body = `{"ts":"` + tc.timestamp + `","cpu_millicores":1,"memory_bytes":2}`
			}
			before := time.Now()
			f.call(t, "POST", "/api/v1/runs/run/jobs/job/metrics", body, "", tc.status, nil)
			after := time.Now()
			if tc.status == 400 {
				if writer.calls != 0 {
					t.Fatal("invalid sample reached transaction")
				}
				return
			}
			if writer.calls != 1 {
				t.Fatalf("writes=%d", writer.calls)
			}
			if tc.timestamp == "" {
				if writer.sample.TS.Before(before) || writer.sample.TS.After(after) {
					t.Fatalf("receipt time=%v", writer.sample.TS)
				}
			} else if !writer.sample.TS.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
				t.Fatalf("timestamp changed: %v", writer.sample.TS)
			}
		})
	}
}

func TestMetricsHTTPAuthorityProperty(t *testing.T) {
	f, writer := metricHTTPFixture(t)
	base := f.facts.snapshot.Authorities[0].Principal
	for _, operation := range []string{"metrics", "usage"} {
		t.Run(operation, func(t *testing.T) {
			property := func(input []byte, phase uint8) bool {
				f.facts.snapshot.Authorities[0].Principal = base
				writer.calls, writer.err, writer.team = 0, nil, store.DefaultTeam
				testOnlyTenantPort[metricWriter](f.s, func(context.Context, store.Team) (metricWriter, error) { return writer, nil })
				want := 204
				body := `{"cpu_time_nanos":2}`
				if operation == "metrics" {
					body = `{"kind":"command","cpu_time_nanos":2,"memory_bytes":3}`
				}
				switch phase % 9 {
				case 1:
					f.facts.snapshot.Authorities[0].Principal.Capability.Grants = nil
					want = 403
				case 2:
					writer.err = &apierr.Error{Code: apierr.LockHeld, Message: "holder changed"}
					want = 409
				case 3:
					writer.err = errors.New("private-backend-secret")
					want = 500
				case 4:
					writer.team = "foreign"
					want = 500
				case 5:
					testOnlyTenantPort[metricWriter](f.s, func(context.Context, store.Team) (metricWriter, error) { return nil, nil })
					want = 500
				case 6:
					body = `{"cpu_time_nanos":-1}`
					want = 400
				case 7:
					body = `{"team":"foreign"}`
					want = 400
				case 8:
					f.facts.snapshot.Authorities[0].Principal.Capability.RevokedAt = &f.facts.snapshot.Now
					want = 401
				}
				r := httptest.NewRequest("POST", "/api/v1/runs/run/jobs/job/"+operation+"?team=foreign&holder="+strings.Repeat("x", len(input)%64), strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer "+f.raw)
				w := httptest.NewRecorder()
				f.s.Handler().ServeHTTP(w, r)
				writes := phase%9 == 0 || phase%9 == 2 || phase%9 == 3
				if w.Code != want || (writer.calls == 1) != writes || strings.Contains(w.Body.String(), "private-backend-secret") {
					t.Logf("phase %d=%d %s calls=%d", phase%9, w.Code, w.Body, writer.calls)
					return false
				}
				if writes && (writer.seen.Principal.Authority != base.Authority || writer.seen.Target.Team != string(store.DefaultTeam) || writer.seen.Target.RunID != "run" || writer.seen.Target.NodeID != "job" || writer.seen.Binding != base.Capability.Binding) {
					return false
				}
				return true
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 72}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMetricsHTTPValidationAndLimit(t *testing.T) {
	f, writer := metricHTTPFixture(t)
	for _, body := range []string{`{"kind":"other"}`, `{"kind":"interval","cpu_time_nanos":1}`, `{"memory_bytes":-1}`, `null`, `{} {}`} {
		f.call(t, http.MethodPost, "/api/v1/runs/run/jobs/job/metrics", body, "", 400, nil)
	}
	if writer.calls != 0 {
		t.Fatal("invalid sample reached writer")
	}
	writer.err = store.ErrNodeMetricLimit
	f.call(t, "POST", "/api/v1/runs/run/jobs/job/metrics", `{}`, "", 429, nil)
	f.call(t, "POST", "/api/v1/runs/run/nodes/job/metrics", `{}`, "", 404, nil)
}

func TestMetricsHTTPReadsPagedSamplesFromAuthorizedTenant(t *testing.T) {
	engine, _, _ := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	for i := range 3 {
		engineSQL(t, engine.Store, `INSERT INTO node_metrics(team,run_id,node_id,ts,cpu_millicores,memory_bytes,cpu_time_nanos,kind) VALUES ('a','run','job',?,?,0,0,'interval')`, time.Unix(int64(i+1), 0).UnixNano(), int64(i+1))
	}
	f, _ := metricHTTPFixture(t)
	f.s.store = engine.Store
	f.s.WithTenantResolver(func(ctx context.Context, team store.Team) (TenantHandle, error) {
		return engine.Store.ForTeam(ctx, team)
	})
	f.facts.snapshot.Authorities[0].Principal.Capability.Team = "a"
	f.s.WithEngineController(engine.EngineController)
	var first, second metricsPage
	f.call(t, "GET", "/api/v1/runs/run/jobs/job/metrics?team=foreign&limit=1", "", "", 200, &first)
	if len(first.Points) != 1 || first.Points[0].CPUMillicores != 1 || first.NextCursor != "1970-01-01T00:00:01Z" {
		t.Fatalf("first=%+v", first)
	}
	f.call(t, "GET", "/api/v1/runs/run/jobs/job/metrics?limit=2&cursor="+url.QueryEscape(first.NextCursor), "", "", 200, &second)
	if len(second.Points) != 2 || second.Points[0].CPUMillicores != 2 || second.Points[1].CPUMillicores != 3 || second.NextCursor != "" {
		t.Fatalf("second=%+v", second)
	}
	for _, query := range []string{"limit=0", "limit=10001", "cursor=invalid"} {
		f.call(t, "GET", "/api/v1/runs/run/jobs/job/metrics?"+query, "", "", 400, nil)
	}
	f.call(t, "GET", "/api/v1/runs/run/jobs/foreign/metrics", "", "", 403, nil)
}
