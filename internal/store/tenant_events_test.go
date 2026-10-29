package store

import (
	"encoding/json"
	"errors"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestTenantEventAppendIsolationProperty(t *testing.T) {
	property := func(input string, offset int32, count uint8) bool {
		tenant, now, _ := stateFixture(t)
		other := &Tenant{s: tenant.s, team: "b"}
		if err := other.TestOnlySeedRun(t.Context(), Run{RunSummary: runstate.RunSummary{ID: "foreign", Pipeline: "p", Status: "pending"}}); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Duration(offset))
		payload, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		for range int(count%9) + 1 {
			if err := other.AppendEvent(t.Context(), "run", "job", "observation", payload); !errors.Is(err, ErrNotFound) {
				t.Fatalf("foreign append: %v", err)
			}
			if err := tenant.AppendEvent(t.Context(), "foreign", "", "observation", payload); !errors.Is(err, ErrNotFound) {
				t.Fatalf("foreign selector: %v", err)
			}
			if err := tenant.AppendEvent(t.Context(), "run", "job", "observation", payload); err != nil {
				t.Fatal(err)
			}
		}
		events, err := tenant.ListEventsAfter(t.Context(), "run", 0, 10)
		if err != nil || len(events) != int(count%9)+1 {
			t.Fatalf("events: %+v %v", events, err)
		}
		for i, event := range events {
			if event.Seq != int64(i+1) || !event.TS.Equal(*now) || string(event.Payload) != string(payload) {
				t.Fatalf("event: %+v", event)
			}
		}
		var foreignCount, foreignBytes int64
		if err := tenant.s.db.QueryRow(`SELECT event_count,event_bytes FROM runs WHERE team='b' AND id='foreign'`).Scan(&foreignCount, &foreignBytes); err != nil {
			t.Fatal(err)
		}
		return foreignCount == 0 && foreignBytes == 0
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 25})); err != nil {
		t.Fatal(err)
	}
}

func TestTenantEventAppendFailureRollsBackSequenceAndBytes(t *testing.T) {
	tenant, _, _ := stateFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER refuse_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'refused'); END`)
	if err := tenant.AppendEvent(t.Context(), "run", "job", "observation", []byte(`{}`)); err == nil {
		t.Fatal("refused event succeeded")
	}
	var count, bytes int64
	if err := tenant.s.db.QueryRow(`SELECT event_count,event_bytes FROM runs WHERE team='a' AND id='run'`).Scan(&count, &bytes); err != nil {
		t.Fatal(err)
	}
	if count != 0 || bytes != 0 {
		t.Fatalf("failed append left count=%d bytes=%d", count, bytes)
	}
	rewriteSchemaExec(t, tenant.s.db, `DROP TRIGGER refuse_event`)
	if err := tenant.AppendEvent(t.Context(), "run", "job", "observation", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	events, err := tenant.ListEventsAfter(t.Context(), "run", 0, 10)
	if err != nil || len(events) != 1 || events[0].Seq != 1 {
		t.Fatalf("retry: %+v %v", events, err)
	}
}
