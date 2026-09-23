package logs

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

type logReservation struct {
	counter *storagequota.Client
	auth    string
	res     storagequota.Reservation
	stored  int64
}

// safety: the operator's team is never counted, and neither is a service without an
// archive, which has no free tier; a controller that cannot count answers 503.
func (s *Server) reserveLogBytes(w http.ResponseWriter, r *http.Request, team string, n int64) (*logReservation, bool) {
	if s.archive == nil || s.counter == nil || storagequota.Exempt(team) {
		return &logReservation{}, true
	}
	auth, err := extractCredential(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return nil, false
	}
	res, err := s.counter.Reserve(r.Context(), auth, team, storagequota.KindLogs, n, false)
	if err != nil {
		s.writeQuotaRefusal(w, err)
		return nil, false
	}
	return &logReservation{counter: s.counter, auth: auth, res: res}, true
}

// safety: the commit outlives the request, so a client that hangs up after the write
// leaves no bytes uncounted.
func (l *logReservation) finish(ctx context.Context, logger *slog.Logger) {
	if l.counter == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var err error
	if l.stored > 0 {
		err = l.counter.Commit(ctx, l.auth, l.res, l.stored)
	} else {
		err = l.counter.Release(ctx, l.auth, l.res)
	}
	if err != nil {
		logger.Error("logs storage count", "team", l.res.Team, "stored", l.stored, "err", err)
	}
}

func (s *Server) writeQuotaRefusal(w http.ResponseWriter, err error) {
	var quota *storagequota.QuotaError
	switch {
	case errors.As(err, &quota) && quota.Paused:
		http.Error(w, err.Error(), http.StatusPaymentRequired)
	case errors.As(err, &quota):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	case errors.Is(err, storagequota.ErrUnavailable):
		s.logger.Error("logs storage count", "err", err)
		w.Header().Set("Retry-After", "30")
		http.Error(w, "log storage cannot be counted right now; retry shortly", http.StatusServiceUnavailable)
	default:
		s.logger.Error("logs storage count", "err", err)
		http.Error(w, "count log storage", http.StatusBadGateway)
	}
}
