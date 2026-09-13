package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

// BearerPrincipal labels bytes this cache served to a caller holding its
// bearer token. The cache authenticates one shared token rather than a
// directory of principals, so its budget separates credentialed traffic
// from the open dependency proxy and leaves per-tenant accounting to the
// controller, which knows who each bearer is.
const BearerPrincipal = "bearer"

var egressMeter *egress.Meter

func cacheEgressPrincipal(r *http.Request) string {
	if bearerToken(r) == "" {
		return egress.AnonymousPrincipal
	}
	return BearerPrincipal
}

func metered(class egress.Class, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if egressMeter == nil {
			next(w, r)
			return
		}
		principal := cacheEgressPrincipal(r)
		if err := egressMeter.Check(principal); err != nil {
			writeEgressRefusal(w, r, class, err)
			return
		}
		next(egressMeter.Serve(w, principal, class), r)
	}
}

func writeEgressRefusal(w http.ResponseWriter, r *http.Request, class egress.Class, err error) {
	body := map[string]any{"error": err.Error(), "code": "egress_budget_exceeded"}
	seconds := int64(60)
	var budget *egress.BudgetError
	if errors.As(err, &budget) {
		body["principal"] = budget.Principal
		body["limit_bytes"] = budget.LimitBytes
		body["used_bytes"] = budget.UsedBytes
		body["month"] = budget.Month
		if s := int64(budget.RetryAfter.Seconds()); s > 0 {
			seconds = s
		}
	}
	log.Printf("sparkwing-cache: egress refused: principal=%s class=%s path=%s",
		cacheEgressPrincipal(r), class, r.URL.Path)
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeEgressJSON(w, body)
}

// safety: the refusal body is what tells a client why its download
// stopped, so neither a body that will not marshal nor a write that
// fails leaves without a line saying so.
func writeEgressJSON(w http.ResponseWriter, body map[string]any) {
	buf, err := json.Marshal(body)
	if err != nil {
		log.Printf("sparkwing-cache: egress refusal body: %v", err)
		buf = []byte(`{"error":"egress budget exceeded","code":"egress_budget_exceeded"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	if _, err := w.Write(buf); err != nil {
		log.Printf("sparkwing-cache: egress refusal not delivered: %v", err)
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
		"enabled":                     true,
		"alarm":                       state.Alarm,
		"global_day_bytes":            state.GlobalDayBytes,
		"global_month_bytes":          state.GlobalMonthBytes,
		"daily_alarm_bytes":           state.DailyAlarmBytes,
		"monthly_bytes_per_principal": state.MonthlyBytesPerPrincipal,
		"refused_total":               state.Refused,
	}
	if !state.Alarm {
		return summary, nil
	}
	return summary, []string{fmt.Sprintf(
		"egress: this cache has sent %s today, at or past the %s daily threshold",
		egress.FormatBytes(state.GlobalDayBytes), egress.FormatBytes(state.DailyAlarmBytes))}
}
