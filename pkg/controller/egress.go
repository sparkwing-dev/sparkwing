package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// WithEgressMeter bounds the bytes this controller sends to clients.
// The meter counts artifact downloads, log reads, the live log stream,
// and git proxy fetches against the principal that asked for them, and
// refuses a principal past its monthly budget with 429. A nil meter
// leaves every download unmetered, which is what a controller started
// without an egress budget serves.
func (s *Server) WithEgressMeter(m *egress.Meter) *Server {
	if m != nil {
		m = m.WithLogger(s.logger)
	}
	s.egress = m
	return s
}

// EgressMeter returns the meter this controller counts downloads
// against, or nil when it was given none.
func (s *Server) EgressMeter() *egress.Meter { return s.egress }

// safety: the error member carries the whole reason, because that is the
// member a client prints when a request fails.
type egressRefusalBody struct {
	Error      string `json:"error"`
	Code       string `json:"code"`
	Principal  string `json:"principal,omitempty"`
	LimitBytes int64  `json:"limit_bytes,omitempty"`
	UsedBytes  int64  `json:"used_bytes,omitempty"`
	Month      string `json:"month,omitempty"`
}

// EgressBudgetCode is the `code` member of the 429 body a download over
// the monthly byte budget answers with.
const EgressBudgetCode = "egress_budget_exceeded"

// EgressStreamLimitCode is the `code` member of the 429 body a live log
// stream past the per-principal concurrency cap answers with.
const EgressStreamLimitCode = "egress_stream_limit"

// safety: the meter keys on the principal the bearer resolved to, so a
// controller serving with auth off counts every download in one bucket
// rather than reporting a budget it cannot attribute.
func egressPrincipal(r *http.Request) string {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p == nil || p.Name == "" {
		return egress.AnonymousPrincipal
	}
	return p.Name
}

func (s *Server) metered(class egress.Class, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.egress == nil {
			next.ServeHTTP(w, r)
			return
		}
		principal := egressPrincipal(r)
		if err := s.egress.Check(principal); err != nil {
			s.writeEgressRefusal(w, r, class, err)
			return
		}
		next.ServeHTTP(s.egress.Serve(w, principal, class), r)
	})
}

// safety: one browser tab per node multiplies a live stream without
// limit, so the stream surface carries a concurrency cap the other
// metered routes do not need.
func (s *Server) meteredStream(class egress.Class, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.egress == nil {
			next.ServeHTTP(w, r)
			return
		}
		principal := egressPrincipal(r)
		if err := s.egress.Check(principal); err != nil {
			s.writeEgressRefusal(w, r, class, err)
			return
		}
		release, err := s.egress.OpenStream(principal)
		if err != nil {
			s.writeEgressRefusal(w, r, class, err)
			return
		}
		defer release()
		next.ServeHTTP(s.egress.Serve(w, principal, class), r)
	})
}

func (s *Server) writeEgressRefusal(w http.ResponseWriter, r *http.Request, class egress.Class, err error) {
	body := egressRefusalBody{Error: err.Error(), Code: EgressBudgetCode}
	retryAfter := time.Minute

	var budget *egress.BudgetError
	var stream *egress.StreamLimitError
	switch {
	case errors.As(err, &budget):
		body.Principal, body.LimitBytes = budget.Principal, budget.LimitBytes
		body.UsedBytes, body.Month = budget.UsedBytes, budget.Month
		retryAfter = budget.RetryAfter
	case errors.As(err, &stream):
		body.Code, body.Principal = EgressStreamLimitCode, stream.Principal
		retryAfter = stream.RetryAfter
	}

	s.logger.Warn("egress refused",
		"code", body.Code, "principal", body.Principal,
		"class", string(class), "path", r.URL.Path)
	w.Header().Set("Retry-After", strconv.Itoa(int(retryAfterSeconds(retryAfter))))
	writeJSON(w, http.StatusTooManyRequests, body)
}

func retryAfterSeconds(d time.Duration) int64 {
	if secs := int64(d.Seconds()); secs > 0 {
		return secs
	}
	return 1
}

func (s *Server) handleEgressState(w http.ResponseWriter, _ *http.Request) {
	if s.egress == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	state := s.egress.State()
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "egress": state})
}

// safety: the alarm is the deployment's bill crossing a daily threshold,
// which no single request can answer for, so it reaches an operator as a
// health problem and a warn line rather than as a refusal.
func (s *Server) egressHealth() (map[string]any, []string) {
	if s.egress == nil {
		return map[string]any{"enabled": false}, nil
	}
	state := s.egress.State()
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
		"egress: the deployment has sent %s today, at or past the %s daily threshold",
		egress.FormatBytes(state.GlobalDayBytes), egress.FormatBytes(state.DailyAlarmBytes))}
}

// safety: a restarted controller that started the month over would hand
// every principal a fresh budget, so this month's totals come back from
// the store before the listener binds.
func (s *Server) loadEgressUsage(ctx context.Context) {
	if s.egress == nil || s.store == nil {
		return
	}
	month := time.Now().UTC().Format("2006-01")
	rows, err := s.store.ListEgressUsage(ctx, month)
	if err != nil {
		s.logger.Warn("egress usage reload failed", "err", err)
		return
	}
	usages := make([]egress.Usage, 0, len(rows))
	for _, row := range rows {
		usages = append(usages, egress.Usage{Principal: row.Principal, Month: row.Month, Bytes: row.Bytes})
	}
	s.egress.Restore(usages)
}

// perf: the reaper calls this on its own tick, so metering costs one
// batch of writes per sweep rather than one write per response.
func (s *Server) flushEgressUsage(ctx context.Context) {
	if s.egress == nil || s.store == nil {
		return
	}
	dirty := s.egress.Dirty()
	if len(dirty) == 0 {
		return
	}
	rows := make([]store.EgressUsage, 0, len(dirty))
	for _, u := range dirty {
		rows = append(rows, store.EgressUsage{Principal: u.Principal, Month: u.Month, Bytes: u.Bytes})
	}
	if err := s.store.RecordEgressUsage(ctx, rows); err != nil {
		s.logger.Warn("egress usage flush failed", "err", err, "principals", len(rows))
	}
}
