package cache

import (
	"fmt"
	"log"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

// BearerPrincipal labels bytes this cache served to a caller holding its
// bearer token, and AnonymousPrincipal labels the rest, which is what
// the open dependency proxy answers.
//
// Those two names are the whole principal set this service can resolve,
// because it authenticates one shared token rather than a directory of
// identities. That is why the cache meters and alarms but never refuses:
// a per-principal refusal here would fall on the bearer every runner in
// the fleet shares, stopping every checkout and cache read at once, with
// no recovery until the month rolled. The per-team cap belongs to the
// controller, which knows who each bearer is.
const BearerPrincipal = "bearer"

var egressMeter *egress.Meter

// safety: the package holds one server's state per process, so a second
// New replaces its configuration; an unchanged budget keeps the counters
// it has rather than restarting the day's total behind the alarm.
func setEgressMeter(cfg egress.Config) {
	if egressMeter != nil && egressMeter.Config() == cfg {
		return
	}
	egressMeter = egress.New(cfg)
}

func cacheEgressPrincipal(r *http.Request) string {
	if bearerToken(r) == "" {
		return egress.AnonymousPrincipal
	}
	return BearerPrincipal
}

// safety: this counts and never refuses; [BearerPrincipal] says why a cap
// this service could enforce would fall on every caller at once.
func metered(class egress.Class, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if egressMeter == nil || egress.Bodyless(r) {
			next(w, r)
			return
		}
		egressMeter.Handle(w, r, cacheEgressPrincipal(r), class, next)
	}
}

// safety: the alarm is this pod's bill crossing a daily threshold, which
// no single download can answer for, so it reaches an operator through
// health and a warn line rather than as a refusal.
func egressHealth() (map[string]any, []string) {
	if egressMeter == nil {
		return map[string]any{"enabled": false}, nil
	}
	state := egressMeter.State()
	summary := map[string]any{
		"enabled":            true,
		"enforced":           false,
		"alarm":              state.Alarm,
		"global_day_bytes":   state.GlobalDayBytes,
		"global_month_bytes": state.GlobalMonthBytes,
		"daily_alarm_bytes":  state.DailyAlarmBytes,
	}
	if !state.Alarm {
		return summary, nil
	}
	return summary, []string{fmt.Sprintf(
		"egress: this cache has sent %s today, at or past the %s daily threshold",
		egress.FormatBytes(state.GlobalDayBytes), egress.FormatBytes(state.DailyAlarmBytes))}
}

func logEgressBudgets(cfg egress.Config) {
	if cfg.GlobalDailyAlarmBytes <= 0 {
		return
	}
	log.Printf("sparkwing-cache egress alarm: %d bytes per UTC day; this service meters and alarms and refuses nothing",
		cfg.GlobalDailyAlarmBytes)
}
