package logs

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

// A free team's logs are held to their share of the allowance by the
// controller, which counts what every team stores. An append reserves the
// bytes it will store with the caller's own credential, writes, and commits
// what it wrote; an append that writes nothing releases its reservation.

// logReservation is one append's hold on its team's log share. The zero
// value holds nothing and finishes as a no-op.
type logReservation struct {
	counter *storagequota.Client
	auth    string
	res     storagequota.Reservation
	// stored is what the append wrote; finish commits it, or releases the
	// reservation when it is zero.
	stored int64
}

// reserveLogBytes holds n bytes of team's log share, and answers the
// request itself when it may not: 413 past the share, 402 for a team with
// neither credits nor a slot, and 503 when the controller cannot count a
// team it has not answered funded for recently. The operator's team is not
// counted, and neither is any team of a service without an archive, which
// has no free tier to hold.
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

// finish commits what the append stored, or releases the reservation. It
// outlives the request's context, because a client that hangs up after the
// write must not leave its bytes uncounted.
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
