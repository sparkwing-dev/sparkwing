package pipelines_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func parseSchedule(t *testing.T, body string) *pipelines.ScheduleTrigger {
	t.Helper()
	cfg, err := pipelines.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Find("nightly")
	if p == nil {
		t.Fatal("pipeline nightly not found")
	}
	return p.On.Schedule
}

func TestScheduleTrigger_ScalarForm(t *testing.T) {
	got := parseSchedule(t, `
pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule: "0 3 * * *"
`)
	if got == nil {
		t.Fatal("schedule not decoded")
	}
	if got.Cron != "0 3 * * *" {
		t.Errorf("cron = %q, want %q", got.Cron, "0 3 * * *")
	}
	if got.TZ != "" || got.Overlap != "" || got.CatchUp != "" {
		t.Errorf("scalar form set a policy field: %+v", got)
	}
}

func TestScheduleTrigger_MappingForm(t *testing.T) {
	got := parseSchedule(t, `
pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        cron: "@daily"
        tz: America/Denver
        overlap: queue
        catch_up: 6h
`)
	if got == nil {
		t.Fatal("schedule not decoded")
	}
	want := pipelines.ScheduleTrigger{Cron: "@daily", TZ: "America/Denver", Overlap: "queue", CatchUp: "6h"}
	if *got != want {
		t.Errorf("schedule = %+v, want %+v", *got, want)
	}
}

func TestScheduleTrigger_UnknownKeyRejected(t *testing.T) {
	_, err := pipelines.Parse(strings.NewReader(`
pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        cron: "0 3 * * *"
        timezone: UTC
`))
	if err == nil {
		t.Fatal("Parse accepted an unknown schedule key")
	}
	if !strings.Contains(err.Error(), `"timezone"`) {
		t.Errorf("error does not name the key: %v", err)
	}
}

func TestScheduleTrigger_SequenceRejected(t *testing.T) {
	_, err := pipelines.Parse(strings.NewReader(`
pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule: ["0 3 * * *"]
`))
	if err == nil {
		t.Fatal("Parse accepted a sequence")
	}
	if !strings.Contains(err.Error(), "sequence") {
		t.Errorf("error does not name the node kind: %v", err)
	}
}

func TestScheduleTrigger_ValidationErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block string
		want  string
	}{
		{"empty cron", `      schedule: ""`, "on.schedule.cron is required"},
		{"malformed cron", `      schedule: "0 3 * *"`, "on.schedule.cron"},
		{"unknown zone", "      schedule:\n        cron: \"0 3 * * *\"\n        tz: Mars/Olympus", "on.schedule.tz"},
		{"bad overlap", "      schedule:\n        cron: \"0 3 * * *\"\n        overlap: wait", "on.schedule.overlap"},
		{"unparsable catch_up", "      schedule:\n        cron: \"0 3 * * *\"\n        catch_up: soon", "on.schedule.catch_up"},
		{"catch_up under the floor", "      schedule:\n        cron: \"0 3 * * *\"\n        catch_up: 30s", "on.schedule.catch_up"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pipelines.Parse(strings.NewReader(
				"pipelines:\n  - name: nightly\n    entrypoint: Nightly\n    on:\n" + tc.block + "\n"))
			if err == nil {
				t.Fatalf("Parse accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), `"nightly"`) {
				t.Errorf("error does not name the pipeline: %v", err)
			}
		})
	}
}

func TestScheduleTrigger_Location(t *testing.T) {
	for _, tc := range []struct {
		tz   string
		want *time.Location
	}{
		{"", time.UTC},
		{"UTC", time.UTC},
		{"local", time.Local},
	} {
		got, err := (&pipelines.ScheduleTrigger{TZ: tc.tz}).Location()
		if err != nil {
			t.Fatalf("Location(%q): %v", tc.tz, err)
		}
		if got != tc.want {
			t.Errorf("Location(%q) = %v, want %v", tc.tz, got, tc.want)
		}
	}

	got, err := (&pipelines.ScheduleTrigger{TZ: "America/Denver"}).Location()
	if err != nil {
		t.Fatalf("Location(America/Denver): %v", err)
	}
	if got.String() != "America/Denver" {
		t.Errorf("Location(America/Denver) = %v", got)
	}

	if _, err := (&pipelines.ScheduleTrigger{TZ: "Mars/Olympus"}).Location(); err == nil {
		t.Error("Location accepted an unknown zone")
	}
}

func TestScheduleTrigger_OverlapPolicy(t *testing.T) {
	if got := (&pipelines.ScheduleTrigger{}).OverlapPolicy(); got != pipelines.DefaultScheduleOverlap {
		t.Errorf("OverlapPolicy() = %q, want %q", got, pipelines.DefaultScheduleOverlap)
	}
	if got := (&pipelines.ScheduleTrigger{Overlap: "queue"}).OverlapPolicy(); got != "queue" {
		t.Errorf("OverlapPolicy() = %q, want queue", got)
	}
}

func TestScheduleTrigger_CatchUpDuration(t *testing.T) {
	got, err := (&pipelines.ScheduleTrigger{}).CatchUpDuration()
	if err != nil {
		t.Fatalf("CatchUpDuration: %v", err)
	}
	if got != pipelines.DefaultScheduleCatchUp {
		t.Errorf("CatchUpDuration() = %s, want %s", got, pipelines.DefaultScheduleCatchUp)
	}

	got, err = (&pipelines.ScheduleTrigger{CatchUp: "90m"}).CatchUpDuration()
	if err != nil {
		t.Fatalf("CatchUpDuration: %v", err)
	}
	if got != 90*time.Minute {
		t.Errorf("CatchUpDuration() = %s, want 1h30m0s", got)
	}

	if _, err := (&pipelines.ScheduleTrigger{CatchUp: "soon"}).CatchUpDuration(); err == nil {
		t.Error("CatchUpDuration accepted an unparsable value")
	}
}

func TestScheduleTrigger_NilValidates(t *testing.T) {
	var s *pipelines.ScheduleTrigger
	if err := s.Validate("nightly"); err != nil {
		t.Errorf("a pipeline with no schedule failed validation: %v", err)
	}
}
