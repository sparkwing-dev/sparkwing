package pipelines_test

import (
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func parseSchedule(t *testing.T, body string) pipelines.ScheduleTriggers {
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

// hack: decodes without validating, so the scalar shorthand -- which carries no
// where and so can never validate -- is still readable here.
func decodeSchedule(t *testing.T, body string) pipelines.ScheduleTriggers {
	t.Helper()
	var cfg pipelines.Config
	if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(cfg.Pipelines) != 1 {
		t.Fatalf("got %d pipelines, want 1", len(cfg.Pipelines))
	}
	return cfg.Pipelines[0].On.Schedule
}

func parseScheduleError(t *testing.T, body string) string {
	t.Helper()
	_, err := pipelines.Parse(strings.NewReader(body))
	if err == nil {
		t.Fatal("Parse accepted the config")
	}
	return err.Error()
}

func TestScheduleTriggers_ScalarForm(t *testing.T) {
	got := decodeSchedule(t, `
pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule: "0 3 * * *"
`)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	if got[0].Cron != "0 3 * * *" {
		t.Errorf("cron = %q, want %q", got[0].Cron, "0 3 * * *")
	}
	if got[0].EffectiveName() != "default" {
		t.Errorf("EffectiveName() = %q, want default", got[0].EffectiveName())
	}
}

func TestScheduleTriggers_MappingForm(t *testing.T) {
	got := parseSchedule(t, `
pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        cron: "@daily"
        where: local
        tz: America/Denver
        overlap: queue
        catch_up: 6h
        args:
          region: us-east
`)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	entry := got[0]
	if entry.Cron != "@daily" || entry.Where != "local" || entry.TZ != "America/Denver" ||
		entry.Overlap != "queue" || entry.CatchUp != "6h" {
		t.Errorf("entry = %+v", entry)
	}
	if entry.Args["region"] != "us-east" {
		t.Errorf("args = %v, want region=us-east", entry.Args)
	}
}

func TestScheduleTriggers_ListForm(t *testing.T) {
	got := parseSchedule(t, `
pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        - name: host
          cron: "0 3 * * *"
          where: local
        - name: cluster
          cron: "0 4 * * *"
          where: controller
          args:
            region: us-east
`)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	if got[0].EffectiveName() != "host" || got[0].Where != "local" {
		t.Errorf("first entry = %+v", got[0])
	}
	if got[1].EffectiveName() != "cluster" || got[1].Where != "controller" || got[1].Args["region"] != "us-east" {
		t.Errorf("second entry = %+v", got[1])
	}
}

func TestScheduleTriggers_AbsentIsNil(t *testing.T) {
	got := decodeSchedule(t, `
pipelines:
  - name: nightly
    entrypoint: Nightly
`)
	if got != nil {
		t.Errorf("a pipeline with no on: block decoded %+v", got)
	}
}

func TestScheduleTriggers_UnknownKeyRejected(t *testing.T) {
	msg := parseScheduleError(t, `
pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        cron: "0 3 * * *"
        where: local
        timezone: UTC
`)
	if !strings.Contains(msg, `"timezone"`) {
		t.Errorf("error does not name the key: %s", msg)
	}
}

func TestScheduleTriggers_ScalarListRejected(t *testing.T) {
	msg := parseScheduleError(t, `
pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule: ["0 3 * * *"]
`)
	if !strings.Contains(msg, "mapping") {
		t.Errorf("error does not say a list entry must be a mapping: %s", msg)
	}
}

func TestScheduleTriggers_ValidationErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block string
		want  []string
	}{
		{"empty cron", "      schedule:\n        cron: \"\"\n        where: local",
			[]string{"on.schedule.cron is required"}},
		{"malformed cron", "      schedule:\n        cron: \"0 3 * *\"\n        where: local",
			[]string{"on.schedule.cron"}},
		{"missing where", `      schedule: "0 3 * * *"`,
			[]string{"on.schedule.where is required", "where it fires", `"local"`, `"controller"`, "entry per side"}},
		{"bad where", "      schedule:\n        cron: \"0 3 * * *\"\n        where: laptop",
			[]string{"on.schedule.where", `"laptop"`}},
		{"unknown zone", "      schedule:\n        cron: \"0 3 * * *\"\n        where: local\n        tz: Mars/Olympus",
			[]string{"on.schedule.tz"}},
		{"bad overlap", "      schedule:\n        cron: \"0 3 * * *\"\n        where: local\n        overlap: wait",
			[]string{"on.schedule.overlap"}},
		{"unparsable catch_up", "      schedule:\n        cron: \"0 3 * * *\"\n        where: local\n        catch_up: soon",
			[]string{"on.schedule.catch_up"}},
		{"catch_up under the floor", "      schedule:\n        cron: \"0 3 * * *\"\n        where: local\n        catch_up: 30s",
			[]string{"on.schedule.catch_up"}},
		{"bad name charset", "      schedule:\n        name: Nightly_Run\n        cron: \"0 3 * * *\"\n        where: local",
			[]string{"on.schedule.name must match"}},
		{"overlong name", "      schedule:\n        name: " + strings.Repeat("a", 41) + "\n        cron: \"0 3 * * *\"\n        where: local",
			[]string{"on.schedule.name must be at most 40"}},
		{"bad arg key", "      schedule:\n        cron: \"0 3 * * *\"\n        where: local\n        args:\n          Dry_Run: \"1\"",
			[]string{"on.schedule.args", `"Dry_Run"`}},
		{"unnamed entries in a list",
			"      schedule:\n        - cron: \"0 3 * * *\"\n          where: local\n        - cron: \"0 4 * * *\"\n          where: controller",
			[]string{"on.schedule[0].name is required"}},
		{"duplicate names",
			"      schedule:\n        - name: host\n          cron: \"0 3 * * *\"\n          where: local\n        - name: host\n          cron: \"0 4 * * *\"\n          where: controller",
			[]string{`schedule "host"`, "duplicate schedule name"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := parseScheduleError(t,
				"pipelines:\n  - name: nightly\n    entrypoint: Nightly\n    on:\n"+tc.block+"\n")
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not contain %q", msg, want)
				}
			}
			if !strings.Contains(msg, `"nightly"`) {
				t.Errorf("error does not name the pipeline: %s", msg)
			}
		})
	}
}

func TestScheduleTriggers_NamedDefaultAccepted(t *testing.T) {
	got := parseSchedule(t, `
pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        - name: default
          cron: "0 3 * * *"
          where: local
        - name: cluster
          cron: "0 4 * * *"
          where: controller
`)
	if len(got) != 2 || got[0].EffectiveName() != "default" {
		t.Errorf("explicit default name rejected or mis-parsed: %+v", got)
	}
}

func TestScheduleTrigger_EffectiveName(t *testing.T) {
	if got := (&pipelines.ScheduleTrigger{}).EffectiveName(); got != pipelines.DefaultScheduleName {
		t.Errorf("EffectiveName() = %q, want %q", got, pipelines.DefaultScheduleName)
	}
	if got := (&pipelines.ScheduleTrigger{Name: "cluster"}).EffectiveName(); got != "cluster" {
		t.Errorf("EffectiveName() = %q, want cluster", got)
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

func TestScheduleTriggers_EmptyValidates(t *testing.T) {
	var s pipelines.ScheduleTriggers
	if err := s.Validate("nightly"); err != nil {
		t.Errorf("a pipeline with no schedule failed validation: %v", err)
	}
}
