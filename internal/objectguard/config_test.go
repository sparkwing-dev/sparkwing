package objectguard_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

func TestParseBudgetOverridesOneWindowAndKeepsTheRest(t *testing.T) {
	cfg, err := objectguard.ParseBudget("put:minute=7, get:day=0")
	if err != nil {
		t.Fatalf("ParseBudget: %v", err)
	}
	if got := cfg.Limits[objectguard.ClassPut].PerMinute; got != 7 {
		t.Fatalf("put per-minute is %d, want the override 7", got)
	}
	if got := cfg.Limits[objectguard.ClassPut].PerDay; got != objectguard.DefaultPutPerDay {
		t.Fatalf("put per-day is %d, want the default %d", got, objectguard.DefaultPutPerDay)
	}
	if got := cfg.Limits[objectguard.ClassGet].PerDay; got != 0 {
		t.Fatalf("get per-day is %d, want 0 for no limit", got)
	}
	if !cfg.Enabled || cfg.Reset != objectguard.TripResetDay {
		t.Fatalf("breaker = %v reset %q, want the defaults", cfg.Enabled, cfg.Reset)
	}
	if empty, err := objectguard.ParseBudget(""); err != nil || empty.Limits[objectguard.ClassList].PerMinute != objectguard.DefaultListPerMinute {
		t.Fatalf("empty spec = %+v, %v; want the defaults", empty, err)
	}
}

func TestParseBudgetRejectsAMalformedEntry(t *testing.T) {
	for _, spec := range []string{"put:minute=lots", "delete:day=-1", "put:hour=5", "copy:minute=5", "put=5"} {
		_, err := objectguard.ParseBudget(spec)
		if err == nil || !strings.Contains(err.Error(), objectguard.BudgetFlag) {
			t.Errorf("ParseBudget(%q) = %v, want a refusal naming %s", spec, err, objectguard.BudgetFlag)
		}
	}
}
