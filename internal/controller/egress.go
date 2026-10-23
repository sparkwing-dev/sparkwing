package controller

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// WithEgressMeter configures process-local egress counters, concurrency caps and daily alarms.
func (s *Server) WithEgressMeter(m *egress.Meter) *Server {
	if m != nil {
		m = m.WithLogger(s.logger)
	}
	s.egress = m
	publishedEgress.Store(m)
	return s
}

func (s *Server) meterResponse(w http.ResponseWriter, r *http.Request, class egress.Class, slot egress.Slot, next http.Handler, principal string, team store.Team) {
	// safety: a response the server discards holds no slot and
	// counts nothing.
	if egress.Bodyless(r) {
		next.ServeHTTP(w, r)
		return
	}
	if s.egress != nil && slot != egress.SlotNone {
		// safety: the slot keys on the authenticated principal alone,
		// because a caller names its own pod and a named pod would buy
		// another slot.
		release, err := s.egress.Open(principal, slot)
		if err != nil {
			s.writeEgressRefusal(w, r, class, err)
			return
		}
		defer release()
	}
	var tenant *store.Tenant
	if s.operator != nil {
		var err error
		tenant, err = s.tenantForTeam(r.Context(), team)
		if err != nil {
			s.writeInternalError(w, r, "resolve egress team", err)
			return
		}
	}
	delivered := s.egress.Handle(w, r, principal, class, next)
	if tenant != nil && delivered > 0 {
		ctx := context.WithoutCancel(r.Context())
		err := tenant.AddUsage(ctx, store.UsageDelta{Period: time.Now().UTC().Format("2006-01-02"), ReplicaID: s.leaseHolder, Counter: "egress_bytes", Amount: delivered})
		if err != nil {
			s.logger.Error("record delivered egress failed", "team", tenant.Team(), "bytes", delivered, "err", err)
		}
	}
}

func (s *Server) holdLogScanSlot(w http.ResponseWriter, r *http.Request, principal string) (func(), bool) {
	if egress.Bodyless(r) {
		return func() {}, true
	}
	release, err := s.egress.Open(principal, egress.SlotDownload)
	if err != nil {
		s.writeEgressRefusal(w, r, egress.ClassLog, err)
		return nil, false
	}
	return release, true
}

func (s *Server) writeEgressRefusal(w http.ResponseWriter, r *http.Request, class egress.Class, err error) {
	reason := "egress_download_limit"
	retryAfter := egress.SlotRetryAfter
	var concurrency *egress.ConcurrencyError
	if errors.As(err, &concurrency) {
		if concurrency.Slot != egress.SlotDownload {
			reason = "egress_stream_limit"
		}
		retryAfter = concurrency.RetryAfter
	}
	s.logger.Warn("egress refused", append(requestLogAttrs(r), "reason", reason, "class", string(class))...)
	w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSeconds(retryAfter), 10))
	routepolicy.WritePolicyError(w, &apierr.Error{Code: apierr.RateLimited, Message: "too many concurrent downloads or log streams", Details: map[string]any{"reason": reason}})
}

func retryAfterSeconds(d time.Duration) int64 {
	if secs := int64(d.Seconds()); secs > 0 {
		return secs
	}
	return 1
}

// safety: public health reports the alarm without exposing usage or budget totals.
func (s *Server) egressHealth() (map[string]any, []string) {
	if s.egress == nil {
		return map[string]any{"enabled": false}, nil
	}
	state := s.egress.State()
	summary := map[string]any{"enabled": true, "alarm": state.Alarm}
	if !state.Alarm {
		return summary, nil
	}
	return summary, []string{"egress: daily alarm threshold reached"}
}

func (s *Server) loadEgressUsage(ctx context.Context) {
	if s.egress == nil || s.operator == nil {
		return
	}
	now := time.Now().UTC()
	rows, err := s.operator.EgressUsageAcrossTeams(ctx, now.Format("2006-01"))
	if err != nil {
		s.logger.Error("egress usage reload failed", "err", err)
		return
	}
	usages := make([]egress.Usage, 0, len(rows))
	var monthBytes int64
	for _, row := range rows {
		if row.Bytes > math.MaxInt64-monthBytes {
			s.logger.Error("egress monthly usage overflow")
			return
		}
		monthBytes += row.Bytes
		usages = append(usages, egress.Usage{Principal: egress.TeamPrincipal(string(row.Team), ""), Month: row.Period, Bytes: row.Bytes})
	}
	s.egress.Restore(usages)
	day := now.Format("2006-01-02")
	rows, err = s.operator.EgressUsageAcrossTeams(ctx, day)
	if err != nil {
		s.logger.Error("egress daily usage reload failed", "err", err)
		return
	}
	var bytes int64
	for _, row := range rows {
		if row.Bytes > math.MaxInt64-bytes {
			s.logger.Error("egress daily usage overflow")
			return
		}
		bytes += row.Bytes
	}
	s.egress.RestoreDay(egress.DayUsage{Day: day, Bytes: bytes, Month: now.Format("2006-01"), MonthBytes: monthBytes})
}
