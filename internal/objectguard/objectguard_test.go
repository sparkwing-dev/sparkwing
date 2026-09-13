package objectguard_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

func testConfig(perMinute, perDay int) objectguard.Config {
	cfg := objectguard.Config{Enabled: true, Reset: objectguard.TripResetDay, Limits: map[objectguard.Class]objectguard.Limit{}}
	for _, c := range objectguard.Classes() {
		cfg.Limits[c] = objectguard.Limit{PerMinute: perMinute, PerDay: perDay}
	}
	return cfg
}

func TestAllowTripsOnMinuteBudgetAndNamesIt(t *testing.T) {
	l := objectguard.New(testConfig(3, 0))
	for i := range 3 {
		if err := l.Allow(objectguard.ClassPut); err != nil {
			t.Fatalf("request %d inside the budget was refused: %v", i, err)
		}
	}
	err := l.Allow(objectguard.ClassPut)
	if err == nil {
		t.Fatal("the request past the per-minute budget was allowed")
	}
	if !errors.Is(err, objectguard.ErrBudgetExceeded) {
		t.Fatalf("refusal does not match ErrBudgetExceeded: %v", err)
	}
	var budget *objectguard.BudgetError
	if !errors.As(err, &budget) {
		t.Fatalf("refusal is not a *BudgetError: %T", err)
	}
	if budget.Class != objectguard.ClassPut || budget.Window != objectguard.WindowMinute || budget.Limit != 3 {
		t.Fatalf("refusal names the wrong budget: class=%s window=%s limit=%d", budget.Class, budget.Window, budget.Limit)
	}
}

func TestTrippedWritesFailClosedWhileReadsKeepTheirOwnBudget(t *testing.T) {
	cfg := objectguard.Config{Enabled: true, Reset: objectguard.TripResetDay, Limits: map[objectguard.Class]objectguard.Limit{
		objectguard.ClassPut: {PerMinute: 1},
		objectguard.ClassGet: {PerMinute: 5},
	}}
	l := objectguard.New(cfg)
	if err := l.Allow(objectguard.ClassPut); err != nil {
		t.Fatalf("first put refused: %v", err)
	}
	if err := l.Allow(objectguard.ClassPut); err == nil {
		t.Fatal("the put past its budget was allowed")
	}
	for i := range 5 {
		if err := l.Allow(objectguard.ClassGet); err != nil {
			t.Fatalf("get %d refused while only the put budget is tripped: %v", i, err)
		}
	}
	if err := l.Allow(objectguard.ClassGet); err == nil {
		t.Fatal("the get past its own budget was allowed")
	}
}

func TestRefusedRequestsSpendNoBudget(t *testing.T) {
	l := objectguard.New(testConfig(2, 0))
	for range 2 {
		if err := l.Allow(objectguard.ClassList); err != nil {
			t.Fatalf("request inside the budget refused: %v", err)
		}
	}
	for range 100 {
		if err := l.Allow(objectguard.ClassList); err == nil {
			t.Fatal("a request past the budget was allowed")
		}
	}
	var listed objectguard.ClassState
	for _, cs := range l.State().Classes {
		if cs.Class == objectguard.ClassList {
			listed = cs
		}
	}
	if listed.Allowed != 2 {
		t.Fatalf("allowed total is %d, want the 2 that reached the store", listed.Allowed)
	}
	if listed.Refused != 100 {
		t.Fatalf("refused total is %d, want 100", listed.Refused)
	}
	if listed.Trips != 1 {
		t.Fatalf("trips is %d, want one trip for the class", listed.Trips)
	}
}

func TestResetClearsTheTripAndTheWindows(t *testing.T) {
	l := objectguard.New(testConfig(1, 1))
	if err := l.Allow(objectguard.ClassDelete); err != nil {
		t.Fatalf("first request refused: %v", err)
	}
	if err := l.Allow(objectguard.ClassDelete); err == nil {
		t.Fatal("the request past the budget was allowed")
	}
	if !l.Tripped() {
		t.Fatal("the limiter does not report itself tripped")
	}
	if !l.ResetClass(objectguard.ClassDelete) {
		t.Fatal("ResetClass did not report the class as tripped")
	}
	if l.Tripped() {
		t.Fatal("the limiter is still tripped after a reset")
	}
	if err := l.Allow(objectguard.ClassDelete); err != nil {
		t.Fatalf("a request after the reset was refused: %v", err)
	}
}

func TestDayRollClearsATripOnlyWhenConfiguredTo(t *testing.T) {
	for _, tc := range []struct {
		reset       objectguard.TripReset
		stillFails  bool
		description string
	}{
		{objectguard.TripResetDay, false, "day"},
		{objectguard.TripResetManual, true, "manual"},
	} {
		t.Run(tc.description, func(t *testing.T) {
			cfg := testConfig(0, 1)
			cfg.Reset = tc.reset
			l := objectguard.New(cfg)
			clock := time.Date(2031, 3, 4, 23, 30, 0, 0, time.UTC)
			objectguard.SetClock(l, func() time.Time { return clock })

			if err := l.Allow(objectguard.ClassPut); err != nil {
				t.Fatalf("first request refused: %v", err)
			}
			if err := l.Allow(objectguard.ClassPut); err == nil {
				t.Fatal("the request past the day budget was allowed")
			}
			clock = clock.Add(2 * time.Hour)
			err := l.Allow(objectguard.ClassPut)
			if tc.stillFails && err == nil {
				t.Fatal("a manual-reset breaker cleared itself on the day roll")
			}
			if !tc.stillFails && err != nil {
				t.Fatalf("a day-reset breaker stayed tripped past the day roll: %v", err)
			}
		})
	}
}

func TestDisabledBreakerCountsWithoutRefusing(t *testing.T) {
	cfg := testConfig(1, 0)
	cfg.Enabled = false
	l := objectguard.New(cfg)
	for i := range 10 {
		if err := l.Allow(objectguard.ClassPut); err != nil {
			t.Fatalf("request %d refused while the breaker is off: %v", i, err)
		}
	}
	state := l.State()
	if !state.Tripped {
		t.Fatal("a disabled breaker stopped recording that the budget was passed")
	}
	if state.Enabled {
		t.Fatal("state reports the breaker as enabled")
	}
}

func TestDefaultBudgetsTripAHotLoopInsideAMinute(t *testing.T) {
	l := objectguard.New(objectguard.DefaultConfig())
	sent := 0
	for range objectguard.DefaultPutPerMinute * 2 {
		if err := l.Allow(objectguard.ClassPut); err != nil {
			break
		}
		sent++
	}
	if sent != objectguard.DefaultPutPerMinute {
		t.Fatalf("a loop with no sleep sent %d puts before the default breaker tripped, want %d", sent, objectguard.DefaultPutPerMinute)
	}
}

func TestMinuteTripClearsWhenTheMinuteRolls(t *testing.T) {
	for _, reset := range []objectguard.TripReset{objectguard.TripResetDay, objectguard.TripResetManual} {
		t.Run(string(reset), func(t *testing.T) {
			cfg := testConfig(1, 0)
			cfg.Reset = reset
			l := objectguard.New(cfg)
			clock := time.Date(2031, 3, 4, 10, 0, 30, 0, time.UTC)
			objectguard.SetClock(l, func() time.Time { return clock })

			if err := l.Allow(objectguard.ClassPut); err != nil {
				t.Fatalf("first request refused: %v", err)
			}
			if err := l.Allow(objectguard.ClassPut); err == nil {
				t.Fatal("the request past the per-minute budget was allowed")
			}
			clock = clock.Add(20 * time.Second)
			if err := l.Allow(objectguard.ClassPut); err == nil {
				t.Fatal("the breaker cleared before its minute rolled")
			}
			clock = clock.Add(70 * time.Second)
			if err := l.Allow(objectguard.ClassPut); err != nil {
				t.Fatalf("a minute trip outlived the minute that caused it: %v", err)
			}
		})
	}
}

func TestMinuteRollLeavesADayTripAlone(t *testing.T) {
	cfg := testConfig(0, 1)
	cfg.Reset = objectguard.TripResetDay
	l := objectguard.New(cfg)
	clock := time.Date(2031, 3, 4, 10, 0, 30, 0, time.UTC)
	objectguard.SetClock(l, func() time.Time { return clock })

	if err := l.Allow(objectguard.ClassPut); err != nil {
		t.Fatalf("first request refused: %v", err)
	}
	if err := l.Allow(objectguard.ClassPut); err == nil {
		t.Fatal("the request past the day budget was allowed")
	}
	clock = clock.Add(5 * time.Minute)
	if err := l.Allow(objectguard.ClassPut); err == nil {
		t.Fatal("a minute roll cleared a day trip")
	}
}

func TestResetReportsEveryClassItCleared(t *testing.T) {
	l := objectguard.New(testConfig(1, 0))
	for _, c := range []objectguard.Class{objectguard.ClassPut, objectguard.ClassGet} {
		if err := l.Allow(c); err != nil {
			t.Fatalf("first %s refused: %v", c, err)
		}
		if err := l.Allow(c); err == nil {
			t.Fatalf("the %s past its budget was allowed", c)
		}
	}
	cleared := l.Reset()
	if len(cleared) != 2 {
		t.Fatalf("Reset reported %v, want the two tripped classes", cleared)
	}
	if l.Tripped() {
		t.Fatal("the limiter is still tripped after a reset")
	}
	if again := l.Reset(); len(again) != 0 {
		t.Fatalf("a second reset reported %v, want nothing left to clear", again)
	}
}

func TestRefusalNamesBothRemedies(t *testing.T) {
	l := objectguard.New(testConfig(1, 0))
	_ = l.Allow(objectguard.ClassPut)
	err := l.Allow(objectguard.ClassPut)
	if err == nil {
		t.Fatal("the request past the budget was allowed")
	}
	for _, want := range []string{
		"SPARKWING_OBJECT_STORE_BREAKER=off",
		"SPARKWING_OBJECT_STORE_PUT_PER_MINUTE",
		"sparkwing cluster object-store reset-breaker",
		"belongs to this process alone",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestStallsAreReportedAndCleared(t *testing.T) {
	const path = "objectguard-test stall path"
	objectguard.ClearStall(path)
	t.Cleanup(func() { objectguard.ClearStall(path) })

	objectguard.ReportStall(path, errors.New("bucket refuses every write"))
	found := false
	for _, s := range objectguard.Stalls() {
		if s.Path == path {
			found = true
			if s.Error != "bucket refuses every write" {
				t.Errorf("the stall carries %q", s.Error)
			}
			if s.Since.IsZero() {
				t.Error("the stall carries no time")
			}
		}
	}
	if !found {
		t.Fatal("a reported stall does not appear in Stalls")
	}

	objectguard.ClearStall(path)
	for _, s := range objectguard.Stalls() {
		if s.Path == path {
			t.Fatal("a cleared stall still appears in Stalls")
		}
	}
}
