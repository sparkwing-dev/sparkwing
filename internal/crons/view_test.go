package crons

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestScheduleViewsRedactEveryUnclassifiedArgument(t *testing.T) {
	args := map[string]string{"token": "private-schedule-argument"}
	row := Row{CronSchedule: store.CronSchedule{Args: args, Override: &store.CronOverride{Args: args}}}
	row.Effective.Args = args
	// Overrides and fire history are independent reader surfaces.
	schedule := NewScheduleView(row)
	for _, view := range []any{schedule, NewFireView(store.CronFire{Args: args}, "success")} {
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), args["token"]) || !strings.Contains(string(raw), `"token":"***"`) {
			t.Fatal("schedule reader exposed unclassified arguments")
		}
	}
	if args["token"] != "private-schedule-argument" {
		t.Fatal("display mutated execution arguments")
	}
}
