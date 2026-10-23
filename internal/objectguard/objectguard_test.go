package objectguard

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testConfig(perMinute, perDay int) Config {
	cfg := Config{Enabled: true, Reset: tripResetDay, Limits: map[Class]Limit{}}
	for _, c := range classes() {
		cfg.Limits[c] = Limit{PerMinute: perMinute, PerDay: perDay}
	}
	return cfg
}

func TestAllowTripsOnMinuteBudgetAndNamesIt(t *testing.T) {
	l := New(testConfig(3, 0))
	for i := range 3 {
		if err := l.Allow(classPut); err != nil {
			t.Fatalf("request %d inside the budget was refused: %v", i, err)
		}
	}
	err := l.Allow(classPut)
	if err == nil {
		t.Fatal("the request past the per-minute budget was allowed")
	}
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("refusal does not match ErrBudgetExceeded: %v", err)
	}
	var budget *budgetError
	if !errors.As(err, &budget) {
		t.Fatalf("refusal is not a *BudgetError: %T", err)
	}
	if budget.Class != classPut || budget.Window != windowMinute || budget.Limit != 3 {
		t.Fatalf("refusal names the wrong budget: class=%s window=%s limit=%d", budget.Class, budget.Window, budget.Limit)
	}
}

func TestTrippedWritesFailClosedWhileReadsKeepTheirOwnBudget(t *testing.T) {
	cfg := Config{Enabled: true, Reset: tripResetDay, Limits: map[Class]Limit{
		classPut: {PerMinute: 1},
		classGet: {PerMinute: 5},
	}}
	l := New(cfg)
	if err := l.Allow(classPut); err != nil {
		t.Fatalf("first put refused: %v", err)
	}
	if err := l.Allow(classPut); err == nil {
		t.Fatal("the put past its budget was allowed")
	}
	for i := range 5 {
		if err := l.Allow(classGet); err != nil {
			t.Fatalf("get %d refused while only the put budget is tripped: %v", i, err)
		}
	}
	if err := l.Allow(classGet); err == nil {
		t.Fatal("the get past its own budget was allowed")
	}
}

func TestRefusedRequestsSpendNoBudget(t *testing.T) {
	l := New(testConfig(2, 0))
	for range 2 {
		if err := l.Allow(ClassList); err != nil {
			t.Fatalf("request inside the budget refused: %v", err)
		}
	}
	for range 100 {
		if err := l.Allow(ClassList); err == nil {
			t.Fatal("a request past the budget was allowed")
		}
	}
	var listed ClassState
	for _, cs := range l.State().Classes {
		if cs.Class == ClassList {
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
	l := New(testConfig(1, 1))
	if err := l.Allow(classDelete); err != nil {
		t.Fatalf("first request refused: %v", err)
	}
	if err := l.Allow(classDelete); err == nil {
		t.Fatal("the request past the budget was allowed")
	}
	if !l.tripped() {
		t.Fatal("the limiter does not report itself tripped")
	}
	if !l.resetClass(classDelete) {
		t.Fatal("ResetClass did not report the class as tripped")
	}
	if l.tripped() {
		t.Fatal("the limiter is still tripped after a reset")
	}
	if err := l.Allow(classDelete); err != nil {
		t.Fatalf("a request after the reset was refused: %v", err)
	}
}

func TestDayRollClearsATripOnlyWhenConfiguredTo(t *testing.T) {
	for _, tc := range []struct {
		reset       TripReset
		stillFails  bool
		description string
	}{
		{tripResetDay, false, "day"},
		{tripResetManual, true, "manual"},
	} {
		t.Run(tc.description, func(t *testing.T) {
			cfg := testConfig(0, 1)
			cfg.Reset = tc.reset
			l := New(cfg)
			clock := time.Date(2031, 3, 4, 23, 30, 0, 0, time.UTC)
			setClock(l, func() time.Time { return clock })

			if err := l.Allow(classPut); err != nil {
				t.Fatalf("first request refused: %v", err)
			}
			if err := l.Allow(classPut); err == nil {
				t.Fatal("the request past the day budget was allowed")
			}
			clock = clock.Add(2 * time.Hour)
			err := l.Allow(classPut)
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
	l := New(cfg)
	for i := range 10 {
		if err := l.Allow(classPut); err != nil {
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
	l := New(defaultConfig())
	sent := 0
	for range defaultPutPerMinute * 2 {
		if err := l.Allow(classPut); err != nil {
			break
		}
		sent++
	}
	if sent != defaultPutPerMinute {
		t.Fatalf("a loop with no sleep sent %d puts before the default breaker tripped, want %d", sent, defaultPutPerMinute)
	}
}

func TestMinuteTripClearsWhenTheMinuteRolls(t *testing.T) {
	for _, reset := range []TripReset{tripResetDay, tripResetManual} {
		t.Run(string(reset), func(t *testing.T) {
			cfg := testConfig(1, 0)
			cfg.Reset = reset
			l := New(cfg)
			clock := time.Date(2031, 3, 4, 10, 0, 30, 0, time.UTC)
			setClock(l, func() time.Time { return clock })

			if err := l.Allow(classPut); err != nil {
				t.Fatalf("first request refused: %v", err)
			}
			if err := l.Allow(classPut); err == nil {
				t.Fatal("the request past the per-minute budget was allowed")
			}
			clock = clock.Add(20 * time.Second)
			if err := l.Allow(classPut); err == nil {
				t.Fatal("the breaker cleared before its minute rolled")
			}
			clock = clock.Add(70 * time.Second)
			if err := l.Allow(classPut); err != nil {
				t.Fatalf("a minute trip outlived the minute that caused it: %v", err)
			}
		})
	}
}

func TestMinuteRollLeavesADayTripAlone(t *testing.T) {
	cfg := testConfig(0, 1)
	cfg.Reset = tripResetDay
	l := New(cfg)
	clock := time.Date(2031, 3, 4, 10, 0, 30, 0, time.UTC)
	setClock(l, func() time.Time { return clock })

	if err := l.Allow(classPut); err != nil {
		t.Fatalf("first request refused: %v", err)
	}
	if err := l.Allow(classPut); err == nil {
		t.Fatal("the request past the day budget was allowed")
	}
	clock = clock.Add(5 * time.Minute)
	if err := l.Allow(classPut); err == nil {
		t.Fatal("a minute roll cleared a day trip")
	}
}

func TestResetReportsEveryClassItCleared(t *testing.T) {
	l := New(testConfig(1, 0))
	for _, c := range []Class{classPut, classGet} {
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
	if l.tripped() {
		t.Fatal("the limiter is still tripped after a reset")
	}
	if again := l.Reset(); len(again) != 0 {
		t.Fatalf("a second reset reported %v, want nothing left to clear", again)
	}
}

func TestRefusalNamesBothRemedies(t *testing.T) {
	l := New(testConfig(1, 0))
	_ = l.Allow(classPut)
	err := l.Allow(classPut)
	if err == nil {
		t.Fatal("the request past the budget was allowed")
	}
	for _, want := range []string{
		"--object-store-budget put:minute",
		"sparkwing operator object-store reset-breaker",
		"belongs to this process alone",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}
