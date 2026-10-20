package acceptance_test

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSurfaceSchedulesTransport(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			calls := 0
			row := controllerwire.ScheduleView{ID: "s1", Name: "api/check/nightly", Pipeline: "check", Where: "controller", Cron: "0 3 * * *", TZ: "UTC", Overlap: "skip", Declared: true, State: "armed", Effective: controllerwire.EffectiveView{Cron: "0 3 * * *", TZ: "UTC", Overlap: "skip"}}
			manager, err := client.NewScheduleManager(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				var response any
				switch calls {
				case 1:
					if req.Method != "GET" || req.URL.RequestURI() != "/api/v1/schedules?all=true&next=2" {
						t.Fatalf("list: %s %s", req.Method, req.URL)
					}
					response = controllerwire.ScheduleOverview{Schedules: []controllerwire.ScheduleView{row}, Upcoming: []controllerwire.ScheduleUpcoming{{Schedule: "s1", Name: row.Name, At: "2026-10-15T03:00:00Z"}}}
				case 2:
					if req.URL.EscapedPath() != "/api/v1/schedules/api%2Fcheck%2Fnightly" || req.URL.Query().Get("fires") != "0" {
						t.Fatalf("read: %s", req.URL)
					}
					response = controllerwire.DetailView{Schedule: row, Fires: []controllerwire.FireView{}, Upcoming: []string{}}
				case 3, 4:
					action := "pause"
					row.Paused = true
					if calls == 4 {
						action = "resume"
						row.Paused = false
					}
					if req.Method != "POST" || req.URL.Path != "/api/v1/schedules/s1/"+action {
						t.Fatalf("pause/resume: %s %s", req.Method, req.URL)
					}
					response = controllerwire.ScheduleEnvelope{Schedule: row}
				case 5:
					if req.Method != "PUT" || req.URL.Path != "/api/v1/schedules/s1/override" {
						t.Fatalf("override: %s %s", req.Method, req.URL)
					}
					var body map[string]any
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body["tz"] != "" || body["catch_up"] != "0s" || !reflect.DeepEqual(body["args"], map[string]any{"empty": "", "message": "a=b"}) {
						t.Fatalf("override presence: %+v", body)
					}
					row.Override = controllerwire.OverrideView{Fields: []string{"tz", "catch_up", "args"}, TZ: "", CatchUpNS: 0, Args: map[string]string{"empty": "", "message": "a=b"}}
					response = controllerwire.ScheduleEnvelope{Schedule: row}
				case 6:
					if req.Method != "DELETE" || req.URL.Path != "/api/v1/schedules/s1/override" {
						t.Fatalf("reset: %s %s", req.Method, req.URL)
					}
					row.Override = controllerwire.OverrideView{}
					response = controllerwire.ScheduleEnvelope{Schedule: row}
				case 7:
					if req.Method != "POST" || req.URL.Path != "/api/v1/schedules/s1/run" || req.Header.Get("Idempotency-Key") != "stable-key" {
						t.Fatalf("run: %s %s", req.Method, req.URL)
					}
					response = controllerwire.ScheduleRun{RunID: "r1", Schedule: row, Replayed: true}
				case 8:
					if req.Method != "DELETE" || req.URL.Path != "/api/v1/schedules/s1" {
						t.Fatalf("delete: %s %s", req.Method, req.URL)
					}
					response = controllerwire.ScheduleDeletion{ScheduleID: "s1", Deleted: false}
				default:
					t.Fatal("unexpected request")
				}
				data, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			listed, err := manager.ListSchedules(t.Context(), controllerwire.ScheduleQuery{All: true, Next: 2})
			if err != nil || len(listed.Upcoming) != 1 || listed.Schedules[0].ID != "s1" {
				t.Fatalf("list: %+v %v", listed, err)
			}
			detail, err := manager.ReadSchedule(t.Context(), row.Name, controllerwire.ScheduleReadQuery{Fires: 0})
			if err != nil || detail.Schedule.ID != "s1" {
				t.Fatalf("read: %+v %v", detail, err)
			}
			for _, pause := range []bool{true, false} {
				got, err := manager.SetSchedulePaused(t.Context(), "s1", pause)
				if err != nil || got.Paused != pause {
					t.Fatalf("pause=%t: %+v %v", pause, got, err)
				}
			}
			tz, catchUp := "", "0s"
			args := map[string]string{"empty": "", "message": "a=b"}
			_, err = manager.OverrideSchedule(t.Context(), "s1", controllerwire.ScheduleOverride{TZ: &tz, CatchUp: &catchUp, Args: &args})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = manager.ResetSchedule(t.Context(), "s1"); err != nil {
				t.Fatal(err)
			}
			ran, err := manager.RunSchedule(t.Context(), "s1", "stable-key")
			if err != nil || ran.RunID != "r1" || !ran.Replayed {
				t.Fatalf("run: %+v %v", ran, err)
			}
			deleted, err := manager.DeleteSchedule(t.Context(), "s1")
			if err != nil || deleted.Deleted || calls != 8 {
				t.Fatalf("delete: %+v %v", deleted, err)
			}
		})
	}
}
