package controller_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestControllerScheduleFiresThroughConcreteStore(t *testing.T) {
	var now time.Time
	f := newBrowserFixture(t, nil, teststore.WithClock(func() time.Time {
		if now.IsZero() {
			return time.Now()
		}
		return now
	}))
	sha := sourcetest.FixedRemote(t, "https://gitlab.com/acme/ci.git")
	now = time.Now().UTC().Add(time.Minute).Truncate(time.Minute)
	body := `{"mode":"replace","repo_url":"https://gitlab.com/acme/ci.git","branch":"main","sha":"` + sha + `","follow":false,"schedules":[{"pipeline":"nightly","cron":"* * * * *","args":{"channel":"nightly"}}]}`
	response := f.serve(f.cookieRequest("PUT", "/api/v1/schedules/repos", body))
	var written controllerwire.ScheduleRepoWritten
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &written) != nil || len(written.Schedules) != 1 {
		t.Fatalf("concrete schedule registration: %d %s", response.Code, response.Body)
	}
	tenant := teststore.ForTeam(t, f.st, store.DefaultTeam)
	row, err := tenant.GetCronSchedule(t.Context(), written.Schedules[0].ID)
	if err != nil || row.NextDueAt == nil {
		t.Fatalf("schedule: %+v %v", row, err)
	}
	now = *row.NextDueAt
	launcher := f.srv.ScheduleLauncher()
	first, err := launcher.Launch(t.Context(), row, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := launcher.Launch(t.Context(), row, now)
	if err != nil || first != second {
		t.Fatalf("scheduled replay: %s / %s %v", first, second, err)
	}
	run, err := tenant.ReadRun(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	request := run.Request.Request
	if run.Request.Hash == "" || request.Origin != runspec.OriginSchedule || request.Dispatch != runspec.DispatchController || request.Actor.ID != "alice" || request.RequestedArgs["channel"] != "nightly" || request.Git.SHA != sha || request.Source.Location.RemoteURL != "https://gitlab.com/acme/ci.git" || len(run.Request.AllowedPools) != 0 || request.Git.App.Set {
		t.Fatalf("scheduled Cloud intake lost source or consent: %+v", run.Request)
	}
	acquired, err := source.ForRequest(run.Request)
	if err != nil || acquired.PipelineSHA != request.Git.SHA {
		t.Fatalf("scheduled source is not pinned for acquisition: pipeline=%s checkout=%s err=%v", acquired.PipelineSHA, acquired.SHA, err)
	}
	response = f.serve(f.cookieRequest("PUT", "/api/v1/schedules/"+row.ID+"/override", `{"args":{"channel":"changed"}}`))
	if response.Code != 200 {
		t.Fatalf("override: %d %s", response.Code, response.Body)
	}
	now = now.Add(time.Minute)
	if id, err := launcher.Launch(t.Context(), row, now); err == nil || id != "" {
		t.Fatalf("stale tick listing fired updated inputs: %s %v", id, err)
	}
}
