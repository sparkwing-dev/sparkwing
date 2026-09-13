package objectguard

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// EnvPrefix prefixes every environment variable this package reads.
const EnvPrefix = "SPARKWING_OBJECT_STORE_"

// EnvBreaker switches the breaker off for one process. An operator who
// cannot reach the controller reset verb, or who needs a local command
// to finish past a tripped budget, sets it to "off". Requests are still
// counted, so the metrics keep telling the truth.
const EnvBreaker = EnvPrefix + "BREAKER"

// EnvTripReset chooses what clears a tripped class without an operator:
// "day" when the day window rolls, "manual" never.
const EnvTripReset = EnvPrefix + "TRIP_RESET"

// ConfigFromEnv layers environment overrides onto DefaultConfig. Each
// class and window has its own variable, spelled
// SPARKWING_OBJECT_STORE_PUT_PER_MINUTE and so on for GET, LIST and
// DELETE and for PER_DAY. It reports the first malformed value rather
// than silently keeping a default, because a typo in a budget is a
// budget that does not apply.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := DefaultConfig()
	for _, c := range Classes() {
		limit := cfg.Limits[c]
		perMinute, err := intEnv(getenv, classEnv(c, WindowMinute), limit.PerMinute)
		if err != nil {
			return Config{}, err
		}
		perDay, err := intEnv(getenv, classEnv(c, WindowDay), limit.PerDay)
		if err != nil {
			return Config{}, err
		}
		cfg.Limits[c] = Limit{PerMinute: perMinute, PerDay: perDay}
	}
	switch v := strings.ToLower(strings.TrimSpace(getenv(EnvTripReset))); v {
	case "":
	case string(TripResetDay), string(TripResetManual):
		cfg.Reset = TripReset(v)
	default:
		return Config{}, fmt.Errorf("%s=%q: want %q or %q", EnvTripReset, v, TripResetDay, TripResetManual)
	}
	switch v := strings.ToLower(strings.TrimSpace(getenv(EnvBreaker))); v {
	case "":
	case "on", "1", "true":
		cfg.Enabled = true
	case "off", "0", "false":
		cfg.Enabled = false
	default:
		return Config{}, fmt.Errorf("%s=%q: want \"on\" or \"off\"", EnvBreaker, v)
	}
	return cfg, nil
}

func classEnv(c Class, w Window) string {
	return EnvPrefix + upper(string(c)) + "_PER_" + upper(string(w))
}

func intEnv(getenv func(string) string, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: want a whole number of requests, or 0 for no limit", name, raw)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s=%q: want a whole number of requests, or 0 for no limit", name, raw)
	}
	return n, nil
}

var (
	sharedOnce sync.Once
	shared     *Limiter
	sharedErr  error
)

// Shared returns the process-wide limiter, built once from the
// environment. Every object-store client Sparkwing constructs draws on
// it, so one runaway caller spends the same budget every other caller
// sees. A malformed environment yields a limiter on the built-in
// defaults and the error that explains why.
func Shared() (*Limiter, error) {
	sharedOnce.Do(func() {
		cfg, err := ConfigFromEnv(os.Getenv)
		if err != nil {
			sharedErr = err
			cfg = DefaultConfig()
		}
		shared = New(cfg)
	})
	return shared, sharedErr
}
