package client

import (
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestScheduleManagerRetainsUpcomingAndEffectiveCadence(t *testing.T) {
	row := scheduleTestRow()
	row.Effective.Overlap = "queue"
	row.Effective.CatchUpNS = int64(2 * time.Hour)
	row.Effective.Args = controllerwire.DisplayScheduleArgs(map[string]string{"depth": "deep"})
	second := row
	second.ID, second.Name, second.Pipeline = "s2", "api/sweep/quick", "sweep"
	next := "2026-10-15T03:00:00Z"
	overview := controllerwire.ScheduleOverview{
		Health:    controllerwire.HealthView{Schedules: 2, Armed: 2},
		Schedules: []controllerwire.ScheduleView{row, second},
		Upcoming:  []controllerwire.ScheduleUpcoming{{Schedule: row.ID, Name: row.Name, At: next}},
	}
	detail := controllerwire.DetailView{Schedule: row, Upcoming: []string{next}}
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/v1/schedules" {
			return scheduleTestResponse(t, overview), nil
		}
		if req.Method != "GET" || req.URL.EscapedPath() != "/api/v1/schedules/api%2Fcheck%2Fnightly" || req.URL.Query().Get("next") != "1" {
			t.Fatalf("read selection: %s %s", req.Method, req.URL)
		}
		return scheduleTestResponse(t, detail), nil
	})
	listed, err := c.ListSchedules(t.Context(), controllerwire.ScheduleQuery{Next: 1})
	if err != nil || !reflect.DeepEqual(listed, overview) {
		t.Fatalf("overview lost fields: %+v %v", listed, err)
	}
	read, err := c.ReadSchedule(t.Context(), row.Name, controllerwire.ScheduleReadQuery{Next: 1})
	if err != nil || !reflect.DeepEqual(read, detail) {
		t.Fatalf("cadence or upcoming lost: %+v %v", read, err)
	}
}

func TestScheduleManagerConfirmsCronOverrideAndDeclaredReset(t *testing.T) {
	declared := scheduleTestRow()
	row := declared
	cron := "0 5 * * *"
	row.Override = controllerwire.OverrideView{Fields: []string{"cron"}, Cron: cron}
	row.Effective.Cron = cron
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/v1/schedules/s1/override" {
			t.Fatalf("override selection: %s", req.URL)
		}
		if req.Method == "DELETE" {
			return scheduleTestResponse(t, controllerwire.ScheduleEnvelope{Schedule: declared}), nil
		}
		if req.Method != "PUT" {
			t.Fatalf("override method: %s", req.Method)
		}
		return scheduleTestResponse(t, controllerwire.ScheduleEnvelope{Schedule: row}), nil
	})
	overridden, err := c.OverrideSchedule(t.Context(), row.ID, controllerwire.ScheduleOverride{Cron: &cron})
	if err != nil || overridden.Effective.Cron != cron || overridden.Cron != declared.Cron {
		t.Fatalf("override replaced declaration: %+v %v", overridden, err)
	}
	reset, err := c.ResetSchedule(t.Context(), row.ID)
	if err != nil || !reflect.DeepEqual(reset, declared) {
		t.Fatalf("reset lost declaration: %+v %v", reset, err)
	}
}

func TestScheduleManagerReturnsStructuredNotFound(t *testing.T) {
	calls := 0
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "GET" || req.URL.Path != "/api/v1/schedules/missing" {
			t.Fatalf("missing selection: %s %s", req.Method, req.URL)
		}
		return engineResponse(404, `{"error":{"code":"not_found","message":"schedule not found"}}`), nil
	})
	_, err := c.ReadSchedule(t.Context(), "missing", controllerwire.ScheduleReadQuery{})
	var structured *apierr.Error
	if !errors.As(err, &structured) || structured.Code != apierr.NotFound || calls != 1 {
		t.Fatalf("missing schedule: %v calls=%d", err, calls)
	}
}
