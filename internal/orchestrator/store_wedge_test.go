package orchestrator

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStoreWedgeGuard_TripsAfterContinuousFailureBudget(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	g := newStoreWedgeGuard(5 * time.Minute)
	g.now = func() time.Time { return clock }

	if err := g.fail("resolve waiter", errors.New("database is locked")); err != nil {
		t.Fatalf("first failure tripped immediately: %v", err)
	}
	clock = clock.Add(4 * time.Minute)
	if err := g.fail("resolve waiter", errors.New("database is locked")); err != nil {
		t.Fatalf("failure inside budget tripped: %v", err)
	}
	clock = clock.Add(90 * time.Second)

	err := g.fail("resolve waiter", errors.New("database is locked"))

	if err == nil {
		t.Fatal("failure past budget did not trip")
	}
	for _, want := range []string{"resolve waiter", "5m30s", "database is locked", "box-slots list", "3 consecutive failures"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("terminal error %q missing %q", err, want)
		}
	}
}

func TestStoreWedgeGuard_SuccessResetsTheStreak(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	g := newStoreWedgeGuard(5 * time.Minute)
	g.now = func() time.Time { return clock }

	if err := g.fail("op", errors.New("database is locked")); err != nil {
		t.Fatalf("first failure tripped: %v", err)
	}
	clock = clock.Add(4 * time.Minute)
	g.success()
	clock = clock.Add(4 * time.Minute)
	if err := g.fail("op", errors.New("database is locked")); err != nil {
		t.Fatalf("first failure of the new streak tripped: %v", err)
	}
	clock = clock.Add(4 * time.Minute)

	if err := g.fail("op", errors.New("database is locked")); err != nil {
		t.Fatalf("intermittent failure tripped despite reset: %v", err)
	}
}

func TestStoreWedgeGuard_LockingProtocolIsImmediatelyTerminal(t *testing.T) {
	g := newStoreWedgeGuard(5 * time.Minute)

	err := g.fail("heartbeat", errors.New("SQLITE_PROTOCOL: locking protocol (15)"))

	if err == nil {
		t.Fatal("locking protocol error was not immediately terminal")
	}
	for _, want := range []string{"heartbeat", "locking protocol", "box-slots list"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("terminal error %q missing %q", err, want)
		}
	}
}

func TestStoreWedgeGuard_NonPositiveBudgetDisablesTheTrip(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	g := newStoreWedgeGuard(0)
	g.now = func() time.Time { return clock }

	if err := g.fail("op", errors.New("database is locked")); err != nil {
		t.Fatalf("disabled budget tripped: %v", err)
	}
	clock = clock.Add(24 * time.Hour)
	if err := g.fail("op", errors.New("database is locked")); err != nil {
		t.Fatalf("disabled budget tripped after a day: %v", err)
	}

	if err := g.fail("op", errors.New("locking protocol")); err == nil {
		t.Fatal("locking protocol must stay terminal with the budget disabled")
	}
}

func TestStoreWedgeBudget_EnvResolution(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		want    time.Duration
		wantErr bool
	}{
		{"unset keeps default", "", DefaultStoreWedgeBudget, false},
		{"duration overrides", "90s", 90 * time.Second, false},
		{"zero disables", "0", 0, false},
		{"unparseable errors", "soon", 0, true},
		{"bare integer errors", "300", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(StoreWedgeBudgetEnvVar, tc.env)

			got, err := storeWedgeBudget()

			if tc.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				if !strings.Contains(err.Error(), StoreWedgeBudgetEnvVar) || !strings.Contains(err.Error(), tc.env) {
					t.Errorf("error %q does not name the variable and value", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("storeWedgeBudget: %v", err)
			}
			if got != tc.want {
				t.Errorf("budget = %s, want %s", got, tc.want)
			}
		})
	}
}
