package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestScheduleHTTPClientExplainsAppDeclarationRefusal(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	row, _, err := f.writer.ArmCronSchedule(t.Context(), store.CronSchedule{
		ID: "app-nightly", RepoPath: "https://github.com/acme/widgets.git", Pipeline: "nightly",
		Cron: "0 3 * * *", TZ: "UTC", Where: store.CronWhereController,
		GitHubInstallationID: 7, GitHubRepositoryID: 701, ArmedBy: "github-app:7",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.s.Handler())
	t.Cleanup(server.Close)
	options := client.EngineOptions{URL: server.URL, HTTP: server.Client(), Bearer: func(context.Context) (string, error) { return f.raw, nil }}
	manager, err := client.NewScheduleManager(options)
	if err != nil {
		t.Fatal(err)
	}
	repositories, err := client.NewScheduleRepositoryManager(options)
	if err != nil {
		t.Fatal(err)
	}
	cron := "0 4 * * *"
	for name, mutate := range map[string]func() error{
		"override": func() error {
			_, err := manager.OverrideSchedule(t.Context(), row.ID, cronspec.Override{Cron: &cron})
			return err
		},
		"reset":             func() error { _, err := manager.ResetSchedule(t.Context(), row.ID); return err },
		"delete":            func() error { _, err := manager.DeleteSchedule(t.Context(), row.ID); return err },
		"delete repository": func() error { _, err := repositories.DeleteSchedulesForRepo(t.Context(), row.RepoPath); return err },
	} {
		t.Run(name, func(t *testing.T) {
			err := mutate()
			var refusal *apierr.Error
			if !errors.As(err, &refusal) || refusal.Code != apierr.Forbidden || !strings.Contains(refusal.Message, "edit the declaration in the repository") {
				t.Errorf("App declaration refusal lacks repository remedy: %v", err)
			}
			after, err := f.writer.GetCronSchedule(t.Context(), row.ID)
			if err != nil || !reflect.DeepEqual(after, row) {
				t.Fatalf("refused mutation changed schedule: %+v %v", after, err)
			}
		})
	}
}

func TestScheduleHTTPClientPreservesOwnerAndArmer(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	var pushed controllerwire.ScheduleRepoWritten
	f.call(t, "PUT", "/api/v1/schedules/repos", schedulePushJSON, "", 200, &pushed)
	server := httptest.NewServer(f.s.Handler())
	t.Cleanup(server.Close)
	manager, err := client.NewScheduleManager(client.EngineOptions{URL: server.URL, HTTP: server.Client(), Bearer: func(context.Context) (string, error) { return f.raw, nil }})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := manager.ListSchedules(t.Context(), controllerwire.ScheduleQuery{})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := manager.ReadSchedule(t.Context(), pushed.Schedules[0].ID, controllerwire.ScheduleReadQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range append(listed.Schedules, detail.Schedule) {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["owner"] != string(store.DefaultTeam) || fields["armed_by"] != "pusher" {
			t.Errorf("schedule %s lost owner or armer: %s", row.ID, encoded)
		}
	}
}
