package objectguard

import (
	"strings"
	"testing"
)

func TestParseBudgetOverridesOneWindowAndKeepsTheRest(t *testing.T) {
	cfg, err := ParseBudget("put:minute=7, get:day=0")
	if err != nil {
		t.Fatalf("ParseBudget: %v", err)
	}
	if got := cfg.Limits[classPut].PerMinute; got != 7 {
		t.Fatalf("put per-minute is %d, want the override 7", got)
	}
	if got := cfg.Limits[classPut].PerDay; got != 0 {
		t.Fatalf("put per-day is %d, want no default shared daily stop", got)
	}
	if got := cfg.Limits[classGet].PerDay; got != 0 {
		t.Fatalf("get per-day is %d, want 0 for no limit", got)
	}
	if !cfg.Enabled || cfg.Reset != tripResetDay {
		t.Fatalf("breaker = %v reset %q, want the defaults", cfg.Enabled, cfg.Reset)
	}
	if got := cfg.Limits[classGet].PerMinute; got != 0 {
		t.Fatalf("get per-minute is %d, want no default shared minute stop", got)
	}
}

func TestParseBudgetRejectsAMalformedEntry(t *testing.T) {
	for _, spec := range []string{"put:minute=lots", "delete:day=-1", "put:hour=5", "copy:minute=5", "put=5"} {
		_, err := ParseBudget(spec)
		if err == nil || !strings.Contains(err.Error(), budgetFlag) {
			t.Errorf("ParseBudget(%q) = %v, want a refusal naming %s", spec, err, budgetFlag)
		}
	}
}

func TestSharedBrakesAreOptIn(t *testing.T) {
	cfg, err := ParseBudget("")
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range classes() {
		if got := cfg.Limits[class]; got != (Limit{}) {
			t.Errorf("%s default limit = %+v, want unlimited", class, got)
		}
	}
	unlimited := New(cfg)
	for range 100_000 {
		if err := unlimited.Allow(classPut); err != nil {
			t.Fatalf("default budget refused a put: %v", err)
		}
	}
	cfg, err = ParseBudget("put:minute=1")
	if err != nil {
		t.Fatal(err)
	}
	minute := New(cfg)
	if err := minute.Allow(classPut); err != nil {
		t.Fatal(err)
	}
	if err := minute.Allow(classPut); err == nil {
		t.Fatal("opt-in minute brake did not trip")
	}
	cfg, err = ParseBudget("put:day=1")
	if err != nil {
		t.Fatal(err)
	}
	limiter := New(cfg)
	if err := limiter.Allow(classPut); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Allow(classPut); err == nil {
		t.Fatal("opt-in daily emergency brake did not trip")
	}
	if err := limiter.Allow(classGet); err != nil {
		t.Fatalf("put brake stopped reads: %v", err)
	}
}
