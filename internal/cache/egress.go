package cache

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel/metric"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

// BearerPrincipal labels bytes this cache served to a caller holding its
// operator token, and AnonymousPrincipal labels the open dependency proxy's.
// A grant's bytes are labeled with its team, under teamPrincipal.
//
// The per-team daily cap is counted by the controller, and the operator
// token and the operator's team stay uncapped, because every in-cluster
// runner shares them. The process-wide daily alarm tells the operator when
// this pod's bill runs ahead of plan, whoever the callers are.
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

const teamPrincipalPrefix = "team:"

func teamPrincipal(team string) string { return teamPrincipalPrefix + team }

func cacheEgressPrincipal(r *http.Request) string {
	if team := callerFrom(r).team; team != "" {
		return teamPrincipal(team)
	}
	if bearerToken(r) == "" {
		return egress.AnonymousPrincipal
	}
	return BearerPrincipal
}

func metered(class egress.Class, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if egressMeter == nil || egress.Bodyless(r) {
			next(w, r)
			return
		}
		principal := cacheEgressPrincipal(r)
		team := callerFrom(r).team
		if counter == nil || operatorTeam(team) {
			egressMeter.Handle(w, r, principal, class, next)
			return
		}
		charge := func(ctx context.Context, n int64, record bool) error {
			return counter.ChargeDownload(ctx, counterAuth, team, n, record)
		}
		egress.ChargeTeam(w, r, charge, writeDownloadRefusal, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			egressMeter.Handle(w, r, principal, class, next)
		}))
	}
}

func writeDownloadRefusal(w http.ResponseWriter, err error) {
	var capErr *storagequota.DownloadCapError
	switch {
	case errors.As(err, &capErr):
		log.Printf("egress refused: %v", err)
		w.Header().Set("Retry-After", strconv.FormatInt(max(int64(math.Ceil(capErr.RetryAfter.Seconds())), 1), 10))
		http.Error(w, err.Error(), http.StatusTooManyRequests)
	case errors.Is(err, storagequota.ErrUnavailable):
		log.Printf("warning: team downloads cannot be counted: %v", err)
		w.Header().Set("Retry-After", "30")
		http.Error(w, "team downloads cannot be counted right now; retry shortly", http.StatusServiceUnavailable)
	default:
		log.Printf("warning: charge team download: %v", err)
		http.Error(w, "charge team download", http.StatusBadGateway)
	}
}

// safety: public health reports the alarm without exposing usage or budget totals.
func egressHealth() (map[string]any, []string) {
	if egressMeter == nil {
		return map[string]any{"enabled": false}, nil
	}
	state := egressMeter.State()
	summary := map[string]any{"enabled": true, "alarm": state.Alarm}
	if !state.Alarm {
		return summary, nil
	}
	return summary, []string{"egress: daily alarm threshold reached"}
}

// safety: read at observation time from the meter itself, so the gauges
// cannot drift from what health reports.
func initEgressMetrics() {
	meter := otelutil.Meter("sparkwing-cache")
	dayBytes, err := meter.Int64ObservableGauge("sparkwing.cache.egress_day_bytes",
		metric.WithDescription("Bytes this cache has sent to clients in the current UTC day"), metric.WithUnit("By"))
	alarm, aerr := meter.Int64ObservableGauge("sparkwing.cache.egress_daily_alarm",
		metric.WithDescription("1 while this cache is past its daily egress alarm threshold, which refuses nothing"))
	failed := errors.Join(err, aerr)
	if failed == nil {
		_, failed = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			if egressMeter == nil {
				return nil
			}
			state := egressMeter.State()
			o.ObserveInt64(dayBytes, state.GlobalDayBytes)
			o.ObserveInt64(alarm, boolGauge(state.Alarm))
			return nil
		}, dayBytes, alarm)
	}
	if failed != nil {
		log.Printf("warning: egress metrics unavailable, so the alarm shows only on /health and the log: %v", failed)
	}
}

func logEgressBudgets(cfg egress.Config) {
	if cfg.GlobalDailyAlarmBytes > 0 {
		log.Printf("sparkwing-cache egress alarm: %d bytes per UTC day; the alarm refuses nothing",
			cfg.GlobalDailyAlarmBytes)
	}
}
