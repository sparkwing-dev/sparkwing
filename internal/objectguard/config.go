package objectguard

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// BudgetFlag is the controller flag ParseBudget reads.
const BudgetFlag = "--object-store-budget"

// ParseBudget layers spec onto DefaultConfig. spec is a comma-separated list
// of class:window=count entries, such as "put:minute=600,get:day=0", where
// class is put, get, list or delete, window is minute or day, and 0 leaves
// that window unlimited. A class and window spec does not name keeps its
// default. It reports the first malformed entry rather than keeping a
// default, because a typo in a budget is a budget that does not apply.
func ParseBudget(spec string) (Config, error) {
	cfg := DefaultConfig()
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, raw, ok := strings.Cut(entry, "=")
		class, window, ok2 := strings.Cut(key, ":")
		if !ok || !ok2 {
			return Config{}, fmt.Errorf("%s %q: want class:window=count, such as put:minute=600", BudgetFlag, entry)
		}
		limit, known := cfg.Limits[Class(strings.TrimSpace(class))]
		if !known {
			return Config{}, fmt.Errorf("%s %q: class must be put, get, list or delete", BudgetFlag, entry)
		}
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("%s %q: want a whole number of requests, or 0 for no limit", BudgetFlag, entry)
		}
		switch Window(strings.TrimSpace(window)) {
		case WindowMinute:
			limit.PerMinute = n
		case WindowDay:
			limit.PerDay = n
		default:
			return Config{}, fmt.Errorf("%s %q: window must be minute or day", BudgetFlag, entry)
		}
		cfg.Limits[Class(strings.TrimSpace(class))] = limit
	}
	return cfg, nil
}

var (
	sharedOnce sync.Once
	shared     *Limiter
)

// Shared returns the process-wide limiter: the one [ConfigureShared] built,
// or one on DefaultConfig. Every object-store client Sparkwing constructs
// draws on it, so one runaway caller spends the same budget every other
// caller sees.
func Shared() *Limiter {
	sharedOnce.Do(func() { shared = New(DefaultConfig()) })
	return shared
}

// ConfigureShared builds the process-wide limiter from cfg. It must run
// before anything calls [Shared], and refuses once the limiter exists,
// because clients built before it already hold the old one.
func ConfigureShared(cfg Config) error {
	configured := false
	sharedOnce.Do(func() {
		shared = New(cfg)
		configured = true
	})
	if !configured {
		return errors.New("objectguard: the process-wide object-store budget was already in use when it was configured")
	}
	return nil
}
