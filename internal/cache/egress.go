package cache

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

// BearerPrincipal labels bytes this cache served to a caller holding its
// bearer token, and AnonymousPrincipal labels the rest, which is what
// the open dependency proxy answers.
//
// Those two names are the whole principal set this service resolves, so
// it carries no per-principal budget: a per-principal refusal here would
// fall on every runner in the fleet at once, with no recovery until the
// month rolled. The per-team cap belongs to the controller, which knows
// who each bearer is. The process-wide daily cap is the one refusal the
// cache makes, as the operator's backstop on this pod's bill.
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

// metered counts what next sends and refuses it with 429 once the
// process-wide daily cap is spent.
func metered(class egress.Class, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if egressMeter == nil || egress.Bodyless(r) {
			next(w, r)
			return
		}
		if err := egressMeter.Check(cacheEgressPrincipal(r)); err != nil {
			writeEgressRefusal(w, err)
			return
		}
		egressMeter.Handle(w, r, cacheEgressPrincipal(r), class, next)
	}
}

func writeEgressRefusal(w http.ResponseWriter, err error) {
	retryAfter := int64(60)
	var budget *egress.BudgetError
	if errors.As(err, &budget) {
		retryAfter = max(int64(budget.RetryAfter.Seconds()), 1)
	}
	log.Printf("egress refused: %v", err)
	w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
	http.Error(w, err.Error(), http.StatusTooManyRequests)
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
		"enforced":           state.DailyCapBytes > 0,
		"alarm":              state.Alarm,
		"global_day_bytes":   state.GlobalDayBytes,
		"global_month_bytes": state.GlobalMonthBytes,
		"daily_alarm_bytes":  state.DailyAlarmBytes,
		"daily_cap_bytes":    state.DailyCapBytes,
	}
	if !state.Alarm {
		return summary, nil
	}
	return summary, []string{fmt.Sprintf(
		"egress: this cache has sent %s today, at or past the %s daily threshold",
		egress.FormatBytes(state.GlobalDayBytes), egress.FormatBytes(state.DailyAlarmBytes))}
}

func logEgressBudgets(cfg egress.Config) {
	if cfg.GlobalDailyAlarmBytes > 0 {
		log.Printf("sparkwing-cache egress alarm: %d bytes per UTC day; the alarm refuses nothing",
			cfg.GlobalDailyAlarmBytes)
	}
	if cfg.GlobalDailyCapBytes > 0 {
		log.Printf("sparkwing-cache egress daily cap: %d bytes per UTC day; past it every metered download is refused with 429 until the day rolls",
			cfg.GlobalDailyCapBytes)
	}
}
