package localws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// The dashboard's TypeScript contract, field for field. A shape that drifts
// from web/src/lib/api.ts is a page that renders undefined.
var (
	cronScheduleKeys = []string{
		"armed_at", "catch_up_ns", "cron", "declared", "id", "last_fired_at",
		"last_outcome", "last_run_id", "name", "next_due_at", "overlap",
		"paused", "pipeline", "repo_path", "state", "tz", "updated_at",
	}
	cronHealthKeys = []string{
		"armed", "detail", "last_tick", "paused", "schedules", "tick_stale",
		"timer", "undeclared",
	}
	cronTimerKeys = []string{"binary", "detail", "enabled", "foreign", "installed", "path", "stale"}
	cronTickKeys  = []string{"at", "error", "host", "version"}
	cronFireKeys  = []string{
		"decided_at", "detail", "due_at", "id", "outcome", "run_id",
		"run_status", "schedule_id",
	}
)

type cronFixture struct {
	api    *cronsAPI
	mux    *http.ServeMux
	store  *store.Store
	paths  orchestrator.Paths
	nights string // the schedule id of nightly
	weekly string
}

// newCronFixture arms a throwaway checkout's two schedules against a home of
// this test's own and serves them through the real route table.
func newCronFixture(t *testing.T, readOnly bool) *cronFixture {
	t.Helper()
	home := t.TempDir()
	paths := orchestrator.PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure %s: %v", paths.Root, err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})

	repo := filepath.Join(t.TempDir(), "dotfiles")
	cfgDir := filepath.Join(repo, ".sparkwing")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", cfgDir, err)
	}
	config := `pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        cron: "0 3 * * *"
        tz: America/Denver
        catch_up: 1h
  - name: weekly
    entrypoint: Weekly
    on:
      schedule: "0 4 * * 0"
`
	if err := os.WriteFile(filepath.Join(cfgDir, "sparkwing.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	svc := &crons.Service{Store: st, ArmedBy: "tester@test-host"}
	if _, err := svc.Arm(context.Background(), repo, nil); err != nil {
		t.Fatalf("arm %s: %v", repo, err)
	}

	fx := &cronFixture{
		api:    &cronsAPI{store: st, paths: paths, readOnly: readOnly},
		mux:    http.NewServeMux(),
		store:  st,
		paths:  paths,
		nights: crons.ScheduleID(repo, "nightly"),
		weekly: crons.ScheduleID(repo, "weekly"),
	}
	registerCronRoutes(fx.mux, fx.api)
	return fx
}

// fire records one resolved instant against a schedule, the way a tick does.
func (fx *cronFixture) fire(t *testing.T, scheduleID, outcome, runID, detail string, at time.Time) {
	t.Helper()
	err := fx.store.ResolveCronDue(context.Background(), scheduleID, at, nil, &store.CronFire{
		DueAt:     at,
		DecidedAt: at.Add(800 * time.Millisecond),
		Outcome:   outcome,
		RunID:     runID,
		Detail:    detail,
	}, at)
	if err != nil {
		t.Fatalf("resolve due instant: %v", err)
	}
}

func (fx *cronFixture) do(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func decodeCron(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	return out
}

func wantKeys(t *testing.T, what string, got map[string]any, want []string) {
	t.Helper()
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("%s keys = %v, want %v", what, keys, want)
	}
}

func objectAt(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	child, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want an object", key, parent[key])
	}
	return child
}

func TestCronsOverview_MatchesTheDashboardContract(t *testing.T) {
	fx := newCronFixture(t, false)
	if err := fx.store.SetCronSchedulePaused(context.Background(), fx.weekly, true, time.Now()); err != nil {
		t.Fatalf("pause: %v", err)
	}

	body := decodeCron(t, fx.do(t, http.MethodGet, "/api/v1/crons"))
	wantKeys(t, "overview", body, []string{"health", "schedules"})

	health := objectAt(t, body, "health")
	wantKeys(t, "health", health, cronHealthKeys)
	wantKeys(t, "health.timer", objectAt(t, health, "timer"), cronTimerKeys)
	tick := objectAt(t, health, "last_tick")
	wantKeys(t, "health.last_tick", tick, cronTickKeys)
	if tick["at"] != "" {
		t.Errorf("last_tick.at = %v, want empty on a home that never ticked", tick["at"])
	}
	if health["schedules"] != float64(2) || health["armed"] != float64(1) || health["paused"] != float64(1) {
		t.Errorf("health counts = %+v, want 2 schedules, 1 armed, 1 paused", health)
	}

	schedules, ok := body["schedules"].([]any)
	if !ok || len(schedules) != 2 {
		t.Fatalf("schedules = %v, want two", body["schedules"])
	}
	byID := map[string]map[string]any{}
	for _, raw := range schedules {
		row, isObject := raw.(map[string]any)
		if !isObject {
			t.Fatalf("schedule is %T, want an object", raw)
		}
		wantKeys(t, "schedule", row, cronScheduleKeys)
		byID[row["id"].(string)] = row
	}

	nightly := byID[fx.nights]
	if nightly == nil {
		t.Fatalf("overview has no row for %s: %v", fx.nights, byID)
	}
	if nightly["name"] != "dotfiles/nightly" || nightly["state"] != "armed" || nightly["cron"] != "0 3 * * *" {
		t.Errorf("nightly = %+v", nightly)
	}
	if nightly["tz"] != "America/Denver" || nightly["catch_up_ns"] != float64(time.Hour) {
		t.Errorf("nightly declaration = %+v", nightly)
	}
	if nightly["last_fired_at"] != nil || nightly["last_run_id"] != "" || nightly["last_outcome"] != "" {
		t.Errorf("a schedule that never fired should report null and empties: %+v", nightly)
	}
	next, isString := nightly["next_due_at"].(string)
	if !isString {
		t.Fatalf("next_due_at = %v, want an RFC3339 instant", nightly["next_due_at"])
	}
	if _, err := time.Parse(time.RFC3339, next); err != nil {
		t.Errorf("next_due_at %q: %v", next, err)
	}
	if byID[fx.weekly]["state"] != "paused" || byID[fx.weekly]["paused"] != true {
		t.Errorf("weekly = %+v, want paused", byID[fx.weekly])
	}
}

func TestCronDetail_CarriesFiresRunStatusAndUpcoming(t *testing.T) {
	fx := newCronFixture(t, false)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if err := fx.store.CreateRun(ctx, store.Run{
		ID: "run_abc", Pipeline: "nightly", Status: "passed",
		TriggerSource: "schedule", CreatedAt: now, StartedAt: now,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	fx.fire(t, fx.nights, store.CronOutcomeSkippedOverlap, "", "previous run still holding admission", now.Add(-33*time.Hour))
	fx.fire(t, fx.nights, store.CronOutcomeFired, "run_abc", "", now.Add(-9*time.Hour))
	fx.fire(t, fx.nights, store.CronOutcomeFired, "run_gone", "", now.Add(-time.Hour))

	body := decodeCron(t, fx.do(t, http.MethodGet, "/api/v1/crons/"+fx.nights))
	wantKeys(t, "detail", body, []string{"fires", "schedule", "upcoming"})
	wantKeys(t, "detail.schedule", objectAt(t, body, "schedule"), cronScheduleKeys)

	fires, ok := body["fires"].([]any)
	if !ok || len(fires) != 3 {
		t.Fatalf("fires = %v, want three", body["fires"])
	}
	first := fires[0].(map[string]any)
	wantKeys(t, "fire", first, cronFireKeys)
	if first["run_id"] != "run_gone" {
		t.Errorf("fires are not newest first: %+v", first)
	}
	if first["run_status"] != "" {
		t.Errorf("a fire whose run the store no longer holds should carry no status: %+v", first)
	}
	fired := fires[1].(map[string]any)
	if fired["run_id"] != "run_abc" || fired["run_status"] != "passed" {
		t.Errorf("run status was not joined onto the fire: %+v", fired)
	}
	skipped := fires[2].(map[string]any)
	if skipped["outcome"] != store.CronOutcomeSkippedOverlap || skipped["run_status"] != "" {
		t.Errorf("skipped fire = %+v", skipped)
	}

	upcoming, ok := body["upcoming"].([]any)
	if !ok || len(upcoming) != cronDetailUpcoming {
		t.Fatalf("upcoming = %v, want %d instants", body["upcoming"], cronDetailUpcoming)
	}
	for _, raw := range upcoming {
		at, isString := raw.(string)
		if !isString {
			t.Fatalf("upcoming instant is %T, want a string", raw)
		}
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			t.Errorf("upcoming %q: %v", at, err)
		}
	}
}

func TestCronDetail_ResolvesADisplayName(t *testing.T) {
	fx := newCronFixture(t, false)
	body := decodeCron(t, fx.do(t, http.MethodGet, "/api/v1/crons/dotfiles%2Fnightly"))
	if objectAt(t, body, "schedule")["id"] != fx.nights {
		t.Fatalf("a display name did not resolve to %s: %+v", fx.nights, body["schedule"])
	}
}

func TestCronPauseAndResume_FlipTheRow(t *testing.T) {
	fx := newCronFixture(t, false)

	paused := decodeCron(t, fx.do(t, http.MethodPost, "/api/v1/crons/"+fx.nights+"/pause"))
	wantKeys(t, "pause", paused, []string{"schedule"})
	row := objectAt(t, paused, "schedule")
	if row["paused"] != true || row["state"] != "paused" {
		t.Fatalf("pause did not flip the row: %+v", row)
	}
	stored, err := fx.store.GetCronSchedule(context.Background(), fx.nights)
	if err != nil || !stored.Paused {
		t.Fatalf("stored row = %+v, err %v, want paused", stored, err)
	}

	resumed := decodeCron(t, fx.do(t, http.MethodPost, "/api/v1/crons/"+fx.nights+"/resume"))
	row = objectAt(t, resumed, "schedule")
	if row["paused"] != false || row["state"] != "armed" {
		t.Fatalf("resume did not flip the row back: %+v", row)
	}
}

func TestCronRunNow_ReturnsTheRunItLaunched(t *testing.T) {
	fx := newCronFixture(t, false)
	var gotHome, gotID string
	restore := cronRunNow
	cronRunNow = func(_ context.Context, home, id string) (string, error) {
		gotHome, gotID = home, id
		return "run_launched", nil
	}
	t.Cleanup(func() { cronRunNow = restore })

	body := decodeCron(t, fx.do(t, http.MethodPost, "/api/v1/crons/"+fx.nights+"/run"))
	wantKeys(t, "run", body, []string{"run_id", "schedule"})
	if body["run_id"] != "run_launched" {
		t.Fatalf("run_id = %v, want the launched run", body["run_id"])
	}
	wantKeys(t, "run.schedule", objectAt(t, body, "schedule"), cronScheduleKeys)
	if gotHome != fx.paths.Root || gotID != fx.nights {
		t.Fatalf("launch called with home %q id %q, want %q %q", gotHome, gotID, fx.paths.Root, fx.nights)
	}
}

func TestCronRunNow_ReportsALaunchFailureAsBadGateway(t *testing.T) {
	fx := newCronFixture(t, false)
	restore := cronRunNow
	cronRunNow = func(context.Context, string, string) (string, error) {
		return "", errors.New("no consumer could be started")
	}
	t.Cleanup(func() { cronRunNow = restore })

	rec := fx.do(t, http.MethodPost, "/api/v1/crons/"+fx.nights+"/run")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no consumer") {
		t.Fatalf("body = %q, want the launch failure", rec.Body.String())
	}
}

func TestCronRoutes_ReadOnlyRefusesWritesAndServesReads(t *testing.T) {
	fx := newCronFixture(t, true)

	for _, path := range []string{"/pause", "/resume", "/run"} {
		rec := fx.do(t, http.MethodPost, "/api/v1/crons/"+fx.nights+path)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s = %d, want 403: %s", path, rec.Code, rec.Body.String())
		}
		if strings.TrimSpace(rec.Body.String()) != "dashboard is read-only" {
			t.Errorf("POST %s body = %q", path, rec.Body.String())
		}
	}
	if rec := fx.do(t, http.MethodGet, "/api/v1/crons"); rec.Code != http.StatusOK {
		t.Errorf("GET overview = %d, want 200 in read-only mode", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/api/v1/crons/"+fx.nights); rec.Code != http.StatusOK {
		t.Errorf("GET detail = %d, want 200 in read-only mode", rec.Code)
	}
	if stored, err := fx.store.GetCronSchedule(context.Background(), fx.nights); err != nil || stored.Paused {
		t.Errorf("a refused pause changed the row: %+v, err %v", stored, err)
	}
}

func TestCronRoutes_UnknownScheduleIsNotFound(t *testing.T) {
	fx := newCronFixture(t, false)
	for _, path := range []string{
		"/api/v1/crons/crn_ffffffffffff",
		"/api/v1/crons/no-such-pipeline",
	} {
		rec := fx.do(t, http.MethodGet, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404: %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := fx.do(t, http.MethodPost, "/api/v1/crons/crn_ffffffffffff/pause")
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST pause on an unknown id = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestCronRoutes_WithoutALocalStore(t *testing.T) {
	mux := http.NewServeMux()
	registerCronRoutes(mux, &cronsAPI{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/crons", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 with no store: %s", rec.Code, rec.Body.String())
	}
}
