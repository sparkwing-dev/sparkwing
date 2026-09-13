package objectguard_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

func envFrom(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestConfigFromEnvOverridesOneClassAndKeepsTheRest(t *testing.T) {
	cfg, err := objectguard.ConfigFromEnv(envFrom(map[string]string{
		"SPARKWING_OBJECT_STORE_PUT_PER_MINUTE": "7",
		"SPARKWING_OBJECT_STORE_GET_PER_DAY":    "0",
	}))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
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
	if !cfg.Enabled {
		t.Fatal("the breaker defaults to off")
	}
	if cfg.Reset != objectguard.TripResetDay {
		t.Fatalf("trip reset is %q, want %q", cfg.Reset, objectguard.TripResetDay)
	}
}

func TestConfigFromEnvSwitchesTheBreakerOffForOneProcess(t *testing.T) {
	cfg, err := objectguard.ConfigFromEnv(envFrom(map[string]string{"SPARKWING_OBJECT_STORE_BREAKER": "off"}))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("SPARKWING_OBJECT_STORE_BREAKER=off left the breaker enabled")
	}
}

func TestConfigFromEnvRejectsAMalformedBudget(t *testing.T) {
	for name, value := range map[string]string{
		"SPARKWING_OBJECT_STORE_PUT_PER_MINUTE": "lots",
		"SPARKWING_OBJECT_STORE_DELETE_PER_DAY": "-1",
		"SPARKWING_OBJECT_STORE_TRIP_RESET":     "hourly",
		"SPARKWING_OBJECT_STORE_BREAKER":        "maybe",
	} {
		_, err := objectguard.ConfigFromEnv(envFrom(map[string]string{name: value}))
		if err == nil {
			t.Fatalf("%s=%q was accepted", name, value)
		}
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("the error for %s does not name the variable: %v", name, err)
		}
	}
}
