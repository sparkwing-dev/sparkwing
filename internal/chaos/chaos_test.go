package chaos_test

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/chaos"
)

func seedFromEnv(t *testing.T) int64 {
	t.Helper()
	if s := os.Getenv("SPARKWING_CHAOS_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("bad SPARKWING_CHAOS_SEED %q: %v", s, err)
		}
		return n
	}
	return time.Now().UnixNano()
}

func TestChaos_CI(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos CI run skipped in -short")
	}
	cfg := chaos.CIConfig(seedFromEnv(t))
	if raw := os.Getenv("SPARKWING_CHAOS_CI_DURATION"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			t.Fatalf("bad SPARKWING_CHAOS_CI_DURATION %q: %v", raw, err)
		}
		cfg.Duration = d
	}
	chaos.Run(t, cfg)
}

func TestChaos_Soak(t *testing.T) {
	raw := os.Getenv("SPARKWING_CHAOS_SOAK")
	if raw == "" {
		t.Skip("set SPARKWING_CHAOS_SOAK=<duration> to run the soak scenario")
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("bad SPARKWING_CHAOS_SOAK %q: %v", raw, err)
	}
	chaos.Run(t, chaos.SoakConfig(seedFromEnv(t), d))
}
