package client

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func scheduleTestRow() controllerwire.ScheduleView {
	return controllerwire.ScheduleView{ID: "s1", Name: "api/check/nightly", RepoPath: "https://github.com/acme/api.git", Pipeline: "check", Where: "controller", State: "armed", Declared: true, Cron: "0 3 * * *", TZ: "UTC", Overlap: "skip", Effective: controllerwire.EffectiveView{Cron: "0 3 * * *", TZ: "UTC", Overlap: "skip"}}
}
func scheduleTestResponse(t *testing.T, value any) *http.Response {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return engineResponse(200, string(data))
}
func TestScheduleManagerRejectsMalformedResponses(t *testing.T) {
	row := scheduleTestRow()
	duplicate := controllerwire.ScheduleOverview{Schedules: []controllerwire.ScheduleView{row, row}}
	invalid := row
	invalid.ID = ""
	foreignFire := controllerwire.DetailView{Schedule: row, Fires: []controllerwire.FireView{{ID: "f1", ScheduleID: "other", DueAt: "2026-10-14T03:00:00Z", DecidedAt: "2026-10-14T03:00:00Z"}}}
	for _, test := range []struct {
		name  string
		value any
		read  bool
	}{
		{"missing fields", map[string]any{}, false}, {"missing identity", controllerwire.ScheduleOverview{Schedules: []controllerwire.ScheduleView{invalid}}, false},
		{"duplicate", duplicate, false}, {"foreign upcoming", controllerwire.ScheduleOverview{Schedules: []controllerwire.ScheduleView{row}, Upcoming: []controllerwire.ScheduleUpcoming{{Schedule: "other", Name: "other", At: "2026-10-14T03:00:00Z"}}}, false},
		{"foreign history", foreignFire, true}, {"invalid instant", controllerwire.DetailView{Schedule: row, Upcoming: []string{"yesterday"}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := testEngine(t, func(*http.Request) (*http.Response, error) { return scheduleTestResponse(t, test.value), nil })
			var err error
			if test.read {
				_, err = c.ReadSchedule(t.Context(), "s1", controllerwire.ScheduleReadQuery{})
			} else {
				_, err = c.ListSchedules(t.Context(), controllerwire.ScheduleQuery{})
			}
			if err == nil {
				t.Fatal("malformed observation accepted")
			}
		})
	}
}
func TestScheduleManagerConfirmsPauseAndReset(t *testing.T) {
	row := scheduleTestRow()
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		return scheduleTestResponse(t, controllerwire.ScheduleEnvelope{Schedule: row}), nil
	})
	if _, err := c.SetSchedulePaused(t.Context(), "s1", true); err == nil {
		t.Fatal("unconfirmed pause accepted")
	}
	if _, err := c.ReadSchedule(t.Context(), "other", controllerwire.ScheduleReadQuery{}); err == nil {
		t.Fatal("conflicting identity accepted")
	}
	row.Override.Fields = []string{"tz"}
	if _, err := c.ResetSchedule(t.Context(), "s1"); err == nil {
		t.Fatal("unconfirmed reset accepted")
	}
}
func TestScheduleManagerValidatesBeforeHTTP(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) { t.Fatal("invalid input reached HTTP"); return nil, io.EOF })
	if _, err := c.ReadSchedule(t.Context(), "", controllerwire.ScheduleReadQuery{}); err == nil {
		t.Fatal("missing selector accepted")
	}
	if _, err := c.ListSchedules(t.Context(), controllerwire.ScheduleQuery{Next: -1}); err == nil {
		t.Fatal("negative count accepted")
	}
	if _, err := c.RunSchedule(t.Context(), "s1", ""); err == nil {
		t.Fatal("unkeyed launch accepted")
	}
	bad := "invalid"
	for _, input := range []controllerwire.ScheduleOverride{{}, {Cron: &bad}, {TZ: &bad}, {Overlap: &bad}, {CatchUp: &bad}} {
		if _, err := c.OverrideSchedule(t.Context(), "s1", input); err == nil {
			t.Fatalf("invalid edit accepted: %+v", input)
		}
	}
}
func TestScheduleManagerRetriesOneLaunchWithoutChangingKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		var keys, uris []string
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			calls++
			keys = append(keys, req.Header.Get("Idempotency-Key"))
			uris = append(uris, req.URL.RequestURI())
			if calls == 1 {
				return nil, io.EOF
			}
			return scheduleTestResponse(t, controllerwire.ScheduleRun{RunID: "r1", Schedule: scheduleTestRow(), Replayed: true}), nil
		})
		got, err := c.RunSchedule(t.Context(), "api/check/nightly", "stable-key")
		if err != nil || !got.Replayed || !reflect.DeepEqual(keys, []string{"stable-key", "stable-key"}) || len(uris) != 2 || uris[0] != "/api/v1/schedules/api%2Fcheck%2Fnightly/run" || uris[0] != uris[1] {
			t.Fatalf("launch changed: %+v %v %v %v", got, keys, uris, err)
		}
	})
}
func TestScheduleRepositoryManagement(t *testing.T) {
	calls := 0
	row := scheduleTestRow()
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if req.Method != "PUT" || req.URL.Path != "/api/v1/schedules/repos" {
				t.Fatalf("replace: %s %s", req.Method, req.URL)
			}
			data, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"sha":"original-sha"`) || !strings.Contains(string(data), `"follow":false`) || !strings.Contains(string(data), `"catch_up":"0s"`) {
				t.Fatalf("source or zero lost: %s", data)
			}
			return scheduleTestResponse(t, controllerwire.ScheduleRepoWritten{Schedules: []controllerwire.ScheduleView{row}, Withdrawn: []string{"check/old"}}), nil
		case 2:
			if req.Method != "DELETE" || req.URL.Path != "/api/v1/schedules/repos" || req.URL.Query().Get("repo_url") != row.RepoPath {
				t.Fatalf("withdraw: %s %s", req.Method, req.URL)
			}
			return scheduleTestResponse(t, controllerwire.ScheduleRepoDeletion{RepoURL: row.RepoPath, Removed: 1}), nil
		}
		t.Fatal("extra request")
		return nil, io.EOF
	})
	got, err := c.ReplaceSchedules(t.Context(), controllerwire.ScheduleRepoRequest{RepoURL: row.RepoPath, Branch: "main", SHA: "original-sha", Schedules: []controllerwire.ScheduleDeclaration{{Pipeline: "check", Cron: "0 3 * * *", CatchUp: "0s"}}})
	if err != nil || len(got.Schedules) != 1 || !reflect.DeepEqual(got.Withdrawn, []string{"check/old"}) {
		t.Fatalf("replace: %+v %v", got, err)
	}
	removed, err := c.DeleteSchedulesForRepo(t.Context(), row.RepoPath)
	if err != nil || removed.Removed != 1 || calls != 2 {
		t.Fatalf("withdraw: %+v %v", removed, err)
	}
}
func TestScheduleRepositoryRejectsConflictingDeletion(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		return engineResponse(200, `{"repo_url":"other","removed":1}`), nil
	})
	if _, err := c.DeleteSchedulesForRepo(t.Context(), "acme/api"); err == nil {
		t.Fatal("conflicting repository accepted")
	}
}

func TestScheduleManagerPreservesCanonicalResolutionAndConfirmsOverrides(t *testing.T) {
	row := scheduleTestRow()
	row.ScheduleName = "nightly"
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == "GET" {
			return scheduleTestResponse(t, controllerwire.DetailView{Schedule: row}), nil
		}
		return scheduleTestResponse(t, controllerwire.ScheduleEnvelope{Schedule: row}), nil
	})
	for _, selector := range []string{row.ID, row.Name, "check", "check/nightly"} {
		if _, err := c.ReadSchedule(t.Context(), selector, controllerwire.ScheduleReadQuery{}); err != nil {
			t.Fatalf("canonical resolution %s: %v", selector, err)
		}
	}
	tz, catchUp := "", "0s"
	args := map[string]string{}
	edit := controllerwire.ScheduleOverride{TZ: &tz, CatchUp: &catchUp, Args: &args}
	if _, err := c.OverrideSchedule(t.Context(), "s1", edit); err == nil {
		t.Fatal("lost explicit override was accepted")
	}
	row.Override = controllerwire.OverrideView{Fields: []string{"tz", "catch_up", "args"}, TZ: "", CatchUpNS: 0, Args: args}
	if _, err := c.OverrideSchedule(t.Context(), "s1", edit); err != nil {
		t.Fatalf("confirmed zero/empty edit: %v", err)
	}
}
