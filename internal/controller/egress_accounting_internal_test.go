package controller

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

func TestControllerEgressUsesAuthenticatedTenant(t *testing.T) {
	engine, _ := engineRuntimeStore(t)
	teststore.SeedTeam(t, engine.Store, "b")
	first := New(engine.Store, nil).WithEgressMeter(egress.New(egress.Config{}))
	second := New(engine.Store, nil)
	for i, server := range []*Server{first, second} {
		for _, class := range []egress.Class{egress.ClassArtifact, egress.ClassLog, egress.ClassLogStream} {
			r := httptest.NewRequest(http.MethodGet, "/data?team=b&replica=forged", nil)
			response := httptest.NewRecorder()
			server.meterResponse(response, r, class, egress.SlotNone, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("bytes"))
			}), egress.TeamPrincipal("a", "shared"), "a")
			if response.Code != http.StatusOK {
				t.Fatalf("response=%d: %s", response.Code, response.Body)
			}
		}
		var bytes int64
		if err := teststore.SQL(t, engine.Store).QueryRow("SELECT SUM(bytes) FROM egress_usage WHERE team='a'").Scan(&bytes); err != nil || bytes != int64(15*(i+1)) {
			t.Fatalf("server=%d bytes=%d: %v", i, bytes, err)
		}
	}
	usage, err := engine.Store.AsOperator().EgressUsageAcrossTeams(t.Context(), time.Now().UTC().Format("2006-01"))
	if err != nil {
		t.Fatal(err)
	}
	byTeam := map[store.Team]int64{}
	for _, u := range usage {
		byTeam[u.Team] += u.Bytes
	}
	if byTeam["a"] != 30 || byTeam["b"] != 0 {
		t.Fatalf("usage by team=%v", byTeam)
	}
}

type shortEgressWriter struct {
	*httptest.ResponseRecorder
}

func (w shortEgressWriter) Write(p []byte) (int, error) {
	n, _ := w.ResponseRecorder.Write(p[:min(len(p), 2)])
	return n, errors.New("client disconnected")
}

func TestControllerEgressCountsDeliveredBytesAndResumesAlarm(t *testing.T) {
	engine, _ := engineRuntimeStore(t)
	s := New(engine.Store, nil).WithEgressMeter(egress.New(egress.Config{GlobalDailyAlarmBytes: 2}))
	for _, tc := range []struct {
		method string
		status int
	}{
		{http.MethodGet, http.StatusOK},
		{http.MethodHead, http.StatusOK},
		{http.MethodGet, http.StatusForbidden},
	} {
		r := httptest.NewRequest(tc.method, "/logs", nil)
		ctx, cancel := context.WithCancel(r.Context())
		r = r.WithContext(ctx)
		s.meterResponse(shortEgressWriter{httptest.NewRecorder()}, r, egress.ClassLogStream, egress.SlotLogStream, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte("long response"))
			cancel()
		}), egress.TeamPrincipal("a", "account"), "a")
		cancel()
	}
	restarted := New(engine.Store, nil).WithEgressMeter(egress.New(egress.Config{GlobalDailyAlarmBytes: 2}))
	restarted.loadEgressUsage(t.Context())
	state := restarted.egress.State()
	if state.GlobalDayBytes != 2 || state.GlobalMonthBytes != 2 || !state.Alarm {
		t.Fatalf("restored=%+v", state)
	}
	if state.Persisted {
		t.Fatal("controller retained the principal flush path")
	}
}

func TestControllerEgressFailuresAreLoud(t *testing.T) {
	engine, _ := engineRuntimeStore(t)
	s := New(engine.Store, nil)
	var logs strings.Builder
	s.logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := teststore.SQL(t, engine.Store).Exec("CREATE TRIGGER refuse_egress BEFORE INSERT ON egress_usage BEGIN SELECT RAISE(ABORT,'storage unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/logs", nil)
	s.meterResponse(httptest.NewRecorder(), r, egress.ClassLog, egress.SlotDownload, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("sent"))
	}), egress.TeamPrincipal("a", "account"), "a")
	if !strings.Contains(logs.String(), "record delivered egress failed") {
		t.Fatalf("missing accounting failure: %s", logs.String())
	}
	var rows int
	if err := teststore.SQL(t, engine.Store).QueryRow("SELECT COUNT(*) FROM egress_usage").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("failed counter left %d rows: %v", rows, err)
	}
}

func TestControllerEgressRestoresTotalsBeyondMeterCardinality(t *testing.T) {
	engine, _ := engineRuntimeStore(t)
	count := egress.MaxPrincipals + 2
	if _, err := teststore.SQL(t, engine.Store).Exec(`WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM numbers WHERE n<?) INSERT INTO teams(name,created_at,updated_at) SELECT 'egress-'||n,1,1 FROM numbers`, count); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, engine.Store).Exec("INSERT INTO egress_usage(team,period,bytes,updated_at) SELECT name,?,1,1 FROM teams WHERE name LIKE 'egress-%'", time.Now().UTC().Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	s := New(engine.Store, nil).WithEgressMeter(egress.New(egress.Config{GlobalDailyAlarmBytes: int64(count)}))
	s.loadEgressUsage(t.Context())
	state := s.egress.State()
	if state.GlobalDayBytes != int64(count) || state.GlobalMonthBytes != int64(count) || !state.Alarm {
		t.Fatalf("restored day=%d month=%d alarm=%t, want %d and alarm", state.GlobalDayBytes, state.GlobalMonthBytes, state.Alarm, count)
	}
}

func TestControllerEgressConcurrencyUsesCanonicalError(t *testing.T) {
	s := New(nil, nil).WithEgressMeter(egress.New(egress.Config{MaxDownloadsPerPrincipal: 1}))
	release, err := s.egress.Open("a/account", egress.SlotDownload)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r := httptest.NewRequest(http.MethodGet, "/logs", nil)
	w := httptest.NewRecorder()
	called := false
	s.meterResponse(w, r, egress.ClassLog, egress.SlotDownload, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), "a/account", "a")
	var body apierr.Envelope
	if w.Code != 429 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error == nil || body.Error.Code != apierr.RateLimited || body.Error.Details["reason"] != "egress_download_limit" || w.Header().Get("Retry-After") == "" || called {
		t.Fatalf("refusal=%d %s", w.Code, w.Body)
	}
}
