package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// WithEgressMeter counts the bytes this controller sends to clients.
// The meter counts artifact downloads, log reads, the live log stream,
// and git proxy fetches against the principal that asked for them, holds
// each principal to its concurrency caps, and raises the daily alarm; it
// refuses no byte. A nil meter leaves every download uncounted. A team's
// daily download cap is charged whether or not a meter is set.
func (s *Server) WithEgressMeter(m *egress.Meter) *Server {
	if m != nil {
		m = m.WithLogger(s.logger)
		// safety: the sweep drains this meter whenever the server has a
		// store, and the server's store is fixed at New, so marking it here
		// does not depend on where in the builder chain this call lands.
		if s.store != nil {
			m = m.WithPersistence()
		}
	}
	s.egress = m
	publishedEgress.Store(m)
	return s
}

// EgressMeter returns the meter this controller counts downloads
// against, or nil when it was given none.
func (s *Server) EgressMeter() *egress.Meter { return s.egress }

// safety: the error member carries the whole reason, because that is the
// member a client prints when a request fails.
type egressRefusalBody struct {
	Error     string `json:"error"`
	Code      string `json:"code"`
	Principal string `json:"principal,omitempty"`
}

// EgressStreamLimitCode is the `code` member of the 429 body a live log
// stream past the per-principal concurrency cap answers with.
const EgressStreamLimitCode = "egress_stream_limit"

// EgressDownloadLimitCode is the `code` member of the 429 body a
// download past the per-principal concurrency cap answers with.
const EgressDownloadLimitCode = "egress_download_limit"

// safety: the meter keys on the team and the name the bearer resolved to,
// because a token's name is free text two teams may share; a controller
// serving with auth off counts every download in one bucket.
func egressPrincipal(r *http.Request) string {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p == nil || p.Name == "" {
		return egress.AnonymousPrincipal
	}
	return egress.TeamPrincipal(string(p.Team), p.Name)
}

func (s *Server) metered(class egress.Class, next http.Handler) http.Handler {
	return s.meterOn(class, egress.SlotDownload, next)
}

// safety: the gitcache proxy is the checkout path every node takes, so it
// is byte-metered but holds no slot; a concurrency cap there refuses the
// clone rather than the download it was meant to bound.
func (s *Server) meteredBytes(class egress.Class, next http.Handler) http.Handler {
	return s.meterOn(class, egress.SlotNone, next)
}

// safety: one browser tab per node multiplies a live stream without
// limit, so the stream surface counts its own slot; a stream lasts as
// long as its node and a download does not.
func (s *Server) meteredStream(class egress.Class, next http.Handler) http.Handler {
	return s.meterOn(class, egress.SlotLogStream, next)
}

func (s *Server) meterOn(class egress.Class, slot egress.Slot, next http.Handler) http.Handler {
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.egress == nil {
			next.ServeHTTP(w, r)
			return
		}
		principal := egressPrincipal(r)
		// safety: a response the server discards holds no slot and
		// counts nothing.
		if egress.Bodyless(r) {
			next.ServeHTTP(w, r)
			return
		}
		if slot != egress.SlotNone {
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
		s.egress.Handle(w, r, principal, class, next)
	})
	// safety: viewing a run's logs is never charged to its team, so a team
	// past its download cap can still read why its run failed.
	if class == egress.ClassLog || class == egress.ClassLogStream {
		return counted
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.chargeTeamDownload(w, r, counted)
	})
}

// safety: the team is the credential's, so a download is charged to the team
// that asked for it; the operator's team and a single-team controller are
// never capped and write no row.
func (s *Server) chargeTeamDownload(w http.ResponseWriter, r *http.Request, next http.Handler) {
	p, ok := PrincipalFromContext(r.Context())
	if !s.MultiTeam() || !ok || p == nil {
		next.ServeHTTP(w, r)
		return
	}
	team := store.NormalizeTeam(p.Team)
	if team == "" || team == store.DefaultTeam {
		next.ServeHTTP(w, r)
		return
	}
	charge := func(ctx context.Context, n int64, record bool) error {
		_, err := s.store.ChargeDownload(ctx, store.DownloadCharge{
			Team: team, Bytes: n, Record: record, Now: time.Now(),
			FreeCapBytes: s.downloadFree, FundedCapBytes: s.downloadFunded,
		})
		return err
	}
	refuse := func(w http.ResponseWriter, err error) {
		var capErr *store.DownloadCapError
		if errors.As(err, &capErr) {
			s.logger.Warn("team download refused", append(requestLogAttrs(r), "team", string(team))...)
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSeconds(capErr.RetryAfter), 10))
			writeError(w, http.StatusTooManyRequests, err)
			return
		}
		s.writeInternalError(w, r, "charge team download", err)
	}
	if err := egress.ChargeTeam(w, r, charge, refuse, next); err != nil {
		s.logger.Warn("charge a streamed team download", "team", string(team), "err", err)
	}
}

func (s *Server) writeEgressRefusal(w http.ResponseWriter, r *http.Request, class egress.Class, err error) {
	body := egressRefusalBody{Error: err.Error(), Code: EgressDownloadLimitCode}
	retryAfter := egress.SlotRetryAfter
	var concurrency *egress.ConcurrencyError
	if errors.As(err, &concurrency) {
		body.Code, body.Principal = concurrencyCode(concurrency.Slot), concurrency.Principal
		retryAfter = concurrency.RetryAfter
	}

	s.logger.Warn("egress refused", append(requestLogAttrs(r),
		"code", body.Code, "principal", body.Principal, "class", string(class))...)
	w.Header().Set("Retry-After", strconv.Itoa(int(retryAfterSeconds(retryAfter))))
	writeJSON(w, http.StatusTooManyRequests, body)
}

func concurrencyCode(slot egress.Slot) string {
	if slot == egress.SlotDownload {
		return EgressDownloadLimitCode
	}
	return EgressStreamLimitCode
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

// safety: A day-shaped period cannot collide with the same service's month-shaped row.
const egressDayPrincipal = "(day)"

// safety: a restarted controller that started the month or the day over
// would hand every principal a fresh budget and reopen the daily cap, so
// both come back from the store before the listener binds.
func (s *Server) loadEgressUsage(ctx context.Context) {
	if s.egress == nil || s.store == nil {
		return
	}
	now := time.Now().UTC()
	rows, err := s.store.ListEgressUsage(ctx, now.Format("2006-01"))
	if err != nil {
		s.logger.Warn("egress usage reload failed", "err", err)
		return
	}
	usages := make([]egress.Usage, 0, len(rows))
	for _, row := range rows {
		usages = append(usages, egress.Usage{Principal: row.Principal, Month: row.Month, Bytes: row.Bytes})
	}
	s.egress.Restore(usages)

	day := now.Format("2006-01-02")
	rows, err = s.store.ListEgressUsage(ctx, day)
	if err != nil {
		s.logger.Warn("egress daily usage reload failed", "err", err)
		return
	}
	for _, row := range rows {
		if row.Principal == egressDayPrincipal {
			s.egress.RestoreDay(egress.DayUsage{Day: day, Bytes: row.Bytes})
		}
	}
}

// perf: the reaper calls this on its own tick, so metering costs one
// batch of writes per sweep rather than one write per response, and the
// prune runs once a month rather than on every sweep.
func (s *Server) sweepEgressUsage(ctx context.Context) {
	if s.egress == nil || s.store == nil {
		return
	}
	s.flushEgressUsage(ctx)
	s.pruneEgressUsage(ctx)
}

func (s *Server) flushEgressUsage(ctx context.Context) {
	principals := 0
	err := s.egress.Flush(func(usages []egress.Usage) error {
		principals = len(usages)
		rows := make([]store.EgressUsage, 0, len(usages))
		for _, usage := range usages {
			rows = append(rows, store.EgressUsage{
				Principal: usage.Principal,
				Month:     usage.Month,
				Bytes:     usage.Bytes,
			})
		}
		return s.store.RecordEgressUsage(ctx, rows)
	})
	if err != nil {
		s.logger.Warn("egress usage flush failed", "err", err, "principals", principals)
	}
	err = s.egress.FlushDay(func(usage egress.DayUsage) error {
		return s.store.RecordEgressUsage(ctx, []store.EgressUsage{{
			Principal: egressDayPrincipal,
			Month:     usage.Day,
			Bytes:     usage.Bytes,
		}})
	})
	if err != nil {
		s.logger.Warn("egress daily usage flush failed", "err", err)
	}
}

// safety: the reaper goroutine is the only caller, so the once-a-month
// gate needs no lock; the first sweep after a start prunes and the rest
// of the month's sweeps do not.
func (s *Server) pruneEgressUsage(ctx context.Context) {
	month := time.Now().UTC().Format("2006-01")
	if s.egressPrunedMonth == month {
		return
	}
	s.egressPrunedMonth = month
	cutoff := time.Now().UTC().AddDate(0, -store.EgressUsageRetentionMonths, 0).Format("2006-01")
	n, err := s.store.PruneEgressUsage(ctx, cutoff)
	if err != nil {
		s.logger.Warn("egress usage prune failed", "err", err, "before", cutoff)
		return
	}
	if n > 0 {
		s.logger.Info("pruned egress usage", "rows", n, "before", cutoff)
	}
}
