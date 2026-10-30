package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type scheduleFixture struct {
	controllerwire.ScheduleManager
	rows     map[string]controllerwire.DetailView
	calls    []string
	override cronspec.Override
	keys     []string
	query    controllerwire.ScheduleQuery
}

func (f *scheduleFixture) ReadSchedule(_ context.Context, name string, _ controllerwire.ScheduleReadQuery) (controllerwire.DetailView, error) {
	f.calls = append(f.calls, "read:"+name)
	row, ok := f.rows[name]
	if !ok {
		return row, errors.New("schedule not found")
	}
	return row, nil
}

func (f *scheduleFixture) SetSchedulePaused(_ context.Context, name string, pause bool) (controllerwire.ScheduleView, error) {
	f.calls = append(f.calls, "pause:"+name)
	row := f.rows[name].Schedule
	row.Paused = pause
	return row, nil
}

func (f *scheduleFixture) OverrideSchedule(_ context.Context, name string, input cronspec.Override) (controllerwire.ScheduleView, error) {
	f.calls = append(f.calls, "override:"+name)
	f.override = input
	return f.rows[name].Schedule, nil
}

func (f *scheduleFixture) RunSchedule(_ context.Context, name, key string) (controllerwire.ScheduleRun, error) {
	f.calls = append(f.calls, "run:"+name)
	f.keys = append(f.keys, key)
	return controllerwire.ScheduleRun{RunID: "run-" + name, Schedule: f.rows[name].Schedule, Replayed: true}, nil
}

func (f *scheduleFixture) ListSchedules(_ context.Context, q controllerwire.ScheduleQuery) (controllerwire.ScheduleOverview, error) {
	f.query = q
	result := controllerwire.ScheduleOverview{Schedules: []controllerwire.ScheduleView{}, Upcoming: []controllerwire.ScheduleUpcoming{{Schedule: "s1", Name: "check/nightly", At: "2099-01-01T01:00:00Z"}}}
	if row, ok := f.rows["s1"]; ok {
		result.Schedules = append(result.Schedules, row.Schedule)
	}
	return result, nil
}

func scheduleInvocation(t *testing.T, args ...string) clisurface.Invocation {
	t.Helper()
	inv, err := clisurface.Parse("sparkwing", append([]string{"schedules"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func scheduleFake() *scheduleFixture {
	row := controllerwire.DetailView{Schedule: controllerwire.ScheduleView{ID: "s1", Name: "check/nightly", Pipeline: "check", Where: "local", Declared: true, State: "armed", Effective: controllerwire.EffectiveView{Cron: "0 3 * * *", TZ: "UTC", Overlap: "skip"}}}
	return &scheduleFixture{rows: map[string]controllerwire.DetailView{"s1": row, "check/nightly": row}}
}

func TestScheduleManagementResolvesEverySelectorBeforeMutation(t *testing.T) {
	f := scheduleFake()
	var out bytes.Buffer
	inv := scheduleInvocation(t, "pause", "--schedule", "s1,missing", "-o", "json")
	if err := manageSchedules(t.Context(), inv, f, &out); err == nil || out.Len() != 0 || !reflect.DeepEqual(f.calls, []string{"read:s1", "read:missing"}) {
		t.Fatalf("partial mutation: %v %s", f.calls, out.String())
	}
	f = scheduleFake()
	inv = scheduleInvocation(t, "pause", "--schedule", "check/nightly,s1", "-o", "json")
	if err := manageSchedules(t.Context(), inv, f, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, []string{"read:check/nightly", "read:s1", "pause:s1"}) || strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), `"paused":true`) {
		t.Fatalf("alias double mutation: %v %s", f.calls, out.String())
	}
}

func TestScheduleManagementKeepsOverridePresenceAndExactValues(t *testing.T) {
	f := scheduleFake()
	var out bytes.Buffer
	inv := scheduleInvocation(t, "set", "--schedule", "s1", "--tz=", "--catch-up", "0s", "--arg", "message=a=b,empty=", "-o", "plain")
	if err := manageSchedules(t.Context(), inv, f, &out); err != nil {
		t.Fatal(err)
	}
	got := f.override
	if got.TZ == nil || *got.TZ != "" || got.CatchUp == nil || *got.CatchUp != "0s" || got.Args == nil || !reflect.DeepEqual(*got.Args, map[string]string{"message": "a=b", "empty": ""}) || got.Cron != nil || out.String() != "s1\n" {
		t.Fatalf("lost explicit values: %+v %q", got, out.String())
	}
}

func TestScheduleManagementTimeZonesWithEmptyZoneInfo(t *testing.T) {
	t.Setenv("ZONEINFO", t.TempDir())
	for _, zone := range []string{"America/Boise", "Etc/UTC"} {
		f := scheduleFake()
		var out bytes.Buffer
		inv := scheduleInvocation(t, "set", "--schedule", "s1", "--tz", zone)
		if err := manageSchedules(t.Context(), inv, f, &out); err != nil {
			t.Fatalf("schedule time zone %q: %v", zone, err)
		}
		if f.override.TZ == nil || *f.override.TZ != zone {
			t.Fatalf("time zone override = %v, want %q", f.override.TZ, zone)
		}
	}
}

func TestScheduleManagementUsesAuthoritativeUpcomingAndRunReceipt(t *testing.T) {
	f := scheduleFake()
	var out bytes.Buffer
	inv := scheduleInvocation(t, "list", "--next", "1", "-o", "json")
	if err := manageSchedules(t.Context(), inv, f, &out); err != nil {
		t.Fatal(err)
	}
	if f.query.Next != 1 || !strings.Contains(out.String(), "2099-01-01T01:00:00Z") {
		t.Fatalf("next: %+v %s", f.query, out.String())
	}
	out.Reset()
	inv = scheduleInvocation(t, "run", "--schedule", "s1", "-o", "json")
	if err := manageSchedules(t.Context(), inv, f, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 1 || f.keys[0] == "" || !strings.Contains(out.String(), `"replayed":true`) || !strings.Contains(out.String(), `"run_id":"run-s1"`) {
		t.Fatalf("run receipt: %v %s", f.keys, out.String())
	}
}

func TestScheduleManagementRejectsInvalidSelectionsAndEdits(t *testing.T) {
	for _, args := range [][]string{
		{"list", "--all", "--timer"},
		{"list", "--next", "0"},
		{"show", "--schedule", "s1", "--fires", "-1"},
		{"show", "--schedule", "s1", "--fires", "0", "--next", "2"},
		{"set", "--schedule", "s1"},
		{"set", "--schedule", "s1", "--reset", "--tz", "UTC"},
		{"set", "--schedule", "s1", "--arg", "bad"},
		{"set", "--schedule", "s1", "--arg", "a=1,a=2"},
		{"set", "--schedule", "s1", "--arg", "a=1,"},
		{"set", "--schedule", "s1", "--cron", "bad"},
		{"set", "--schedule", "s1", "--catch-up", "-1s"},
	} {
		inv := scheduleInvocation(t, args...)
		f := scheduleFake()
		var out bytes.Buffer
		err := manageSchedules(t.Context(), inv, f, &out)
		var usage *clisurface.UsageError
		if !errors.As(err, &usage) || len(f.calls) > 0 || out.Len() > 0 {
			t.Fatalf("invalid edit %v: %v %v %s", args, err, f.calls, out.String())
		}
	}
}

func TestScheduleManagementListKeepsSelectionMetadataAndShowKeepsDetails(t *testing.T) {
	f := scheduleFake()
	row := f.rows["s1"]
	row.Schedule.Lock = controllerwire.LockView{State: "ahead", Ref: "original-sha", Binary: "private/path", Digest: "digest"}
	row.Schedule.Args = controllerwire.DisplayScheduleArgs(map[string]string{"oversized": "detail-only"})
	row.Schedule.Override = controllerwire.OverrideView{Fields: []string{"cron"}, Stale: true}
	f.rows["s1"] = row
	var out bytes.Buffer
	inv := scheduleInvocation(t, "list", "-o", "json")
	if err := manageSchedules(t.Context(), inv, f, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "detail-only") || strings.Contains(out.String(), "private/path") || !strings.Contains(out.String(), `"lock_state":"ahead"`) || !strings.Contains(out.String(), `"override_stale":true`) {
		t.Fatalf("list projection: %s", out.String())
	}
	out.Reset()
	inv = scheduleInvocation(t, "show", "--schedule", "s1", "-o", "json")
	if err := manageSchedules(t.Context(), inv, f, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "detail-only") || !strings.Contains(out.String(), `"oversized":"***"`) || !strings.Contains(out.String(), "original-sha") {
		t.Fatalf("show projection: %s", out.String())
	}
}

type scheduleHealthFixture struct {
	scheduleFixture
	health controllerwire.HealthView
}

func (f *scheduleHealthFixture) ListSchedules(context.Context, controllerwire.ScheduleQuery) (controllerwire.ScheduleOverview, error) {
	return controllerwire.ScheduleOverview{Health: f.health}, nil
}

func TestScheduleManagementHealthDoesNotLoseFailureStatus(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		f := &scheduleHealthFixture{health: controllerwire.HealthView{Schedules: 2, Armed: 2, Detail: "evaluator status", Remedy: "start the timer"}}
		f.health.Timer.Enabled = healthy
		f.health.TickStale = !healthy
		inv := scheduleInvocation(t, "list", "--timer", "-o", "json")
		var out bytes.Buffer
		err := manageSchedules(t.Context(), inv, f, &out)
		if (err == nil) != healthy || !strings.Contains(out.String(), `"detail":"evaluator status"`) {
			t.Fatalf("health %t: %s %v", healthy, out.String(), err)
		}
	}
}

func TestScheduleManagementCanClearArgumentOverrides(t *testing.T) {
	f := scheduleFake()
	var out bytes.Buffer
	inv := scheduleInvocation(t, "set", "--schedule", "s1", "--arg=", "-o", "json")
	if err := manageSchedules(t.Context(), inv, f, &out); err != nil {
		t.Fatal(err)
	}
	if f.override.Args == nil || *f.override.Args == nil || len(*f.override.Args) != 0 {
		t.Fatalf("explicit empty args lost: %+v", f.override)
	}
}
