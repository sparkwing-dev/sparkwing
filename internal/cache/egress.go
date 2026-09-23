package cache

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

// BearerPrincipal labels bytes this cache served to a caller holding its
// operator token, and AnonymousPrincipal labels the open dependency proxy's.
// A grant's bytes are labeled with its team, under teamPrincipal.
//
// The per-team daily cap is counted by the controller, and the operator
// token and the operator's team stay unbudgeted, because every in-cluster
// runner shares them. The process-wide daily cap is the operator's backstop
// on this pod's bill whoever the callers are.
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

// metered counts what next sends and refuses it with 429 once the
// process-wide daily cap is spent. A GET by a team's grant is also charged
// to the team's UTC day in the controller before its first byte, and
// refused with 429 past the team's cap.
func metered(class egress.Class, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if egressMeter == nil || egress.Bodyless(r) {
			next(w, r)
			return
		}
		principal := cacheEgressPrincipal(r)
		if err := egressMeter.Check(principal); err != nil {
			writeEgressRefusal(w, err)
			return
		}
		team := callerFrom(r).team
		if counter == nil || operatorTeam(team) || r.Method != http.MethodGet {
			egressMeter.Handle(w, r, principal, class, next)
			return
		}
		charged := &chargedWriter{ResponseWriter: w, ctx: r.Context(), team: team}
		egressMeter.Handle(charged, r, principal, class, next)
		charged.settle()
	}
}

var errDownloadRefused = errors.New("the team's download was refused before its body")

// chargedWriter charges a team's download to its UTC day when the response
// starts. A response that names its length is charged that length whole,
// and refused past the cap before one byte is sent; one that streams is
// checked for room when it starts and charged what it sent when it ends.
type chargedWriter struct {
	http.ResponseWriter
	ctx       context.Context
	team      string
	started   bool
	refused   bool
	streaming bool
	sent      int64
}

func (c *chargedWriter) WriteHeader(code int) {
	if c.started {
		c.ResponseWriter.WriteHeader(code)
		return
	}
	c.started = true
	if code/100 != 2 {
		c.ResponseWriter.WriteHeader(code)
		return
	}
	size, err := strconv.ParseInt(c.Header().Get("Content-Length"), 10, 64)
	if err != nil || size < 0 {
		c.streaming, size = true, 0
	}
	if err := counter.ChargeDownload(c.ctx, counterAuth, c.team, size, false); err != nil {
		c.refused = true
		writeDownloadRefusal(c.ResponseWriter, err)
		return
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *chargedWriter) Write(p []byte) (int, error) {
	if !c.started {
		c.WriteHeader(http.StatusOK)
	}
	if c.refused {
		return 0, errDownloadRefused
	}
	n, err := c.ResponseWriter.Write(p)
	c.sent += int64(n)
	return n, err
}

func (c *chargedWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok && !c.refused {
		f.Flush()
	}
}

func (c *chargedWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// settle charges a streamed response what it sent, which the cap already
// admitted it to start.
func (c *chargedWriter) settle() {
	if !c.streaming || c.sent == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
	defer cancel()
	if err := counter.ChargeDownload(ctx, counterAuth, c.team, c.sent, true); err != nil {
		// #nosec G706 -- the team is a checked slug
		log.Printf("warning: charge team %s's streamed download of %d bytes: %v", c.team, c.sent, err)
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

func writeEgressRefusal(w http.ResponseWriter, err error) {
	retryAfter := int64(60)
	var budget *egress.BudgetError
	if errors.As(err, &budget) {
		retryAfter = max(int64(math.Ceil(budget.RetryAfter.Seconds())), 1)
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
