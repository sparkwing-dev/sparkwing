package logs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

// LogBlockBytes is how much of a team's log share the logs service
// reserves for one run at a time. Appends draw from the block without
// asking the controller, so a run costs about one controller call per
// block, and a team holds at most one block per live run beyond what it
// stores.
const LogBlockBytes int64 = 1 << 20

// LogBlockSettleEvery is how often the logs service commits what each run
// drew from its block. A run with no append for a whole interval gives the
// rest of its block back.
const LogBlockSettleEvery = time.Minute

type logBlockKey struct{ team, run string }

type logBlock struct {
	mu     sync.Mutex
	key    logBlockKey
	auth   string
	res    storagequota.Reservation
	left   int64
	used   int64
	active bool
	closed bool
}

func (b *logBlock) held() bool { return b.res.ID != "" || b.res.Unlimited }

func (b *logBlock) take(n int64) bool {
	if !b.held() || (!b.res.Unlimited && b.left < n) {
		return false
	}
	b.left -= n
	b.used += n
	b.active = true
	return true
}

func (b *logBlock) adopt(res storagequota.Reservation) {
	b.res, b.left, b.used = res, res.Granted, 0
}

type logBlocks struct {
	mu     sync.Mutex
	blocks map[logBlockKey]*logBlock
}

func (l *logBlocks) get(key logBlockKey) *logBlock {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.blocks == nil {
		l.blocks = map[logBlockKey]*logBlock{}
	}
	b, ok := l.blocks[key]
	if !ok {
		b = &logBlock{key: key}
		l.blocks[key] = b
	}
	return b
}

func (l *logBlocks) drop(b *logBlock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.blocks[b.key] == b {
		delete(l.blocks, b.key)
	}
}

func (l *logBlocks) all() []*logBlock {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*logBlock, 0, len(l.blocks))
	for _, b := range l.blocks {
		out = append(out, b)
	}
	return out
}

type logDraw struct {
	block  *logBlock
	n      int64
	stored int64
}

// safety: a draw the append did not write goes back to the block, so a
// refused or failed write costs the team nothing.
func (d *logDraw) finish() {
	if d.block == nil || d.stored >= d.n {
		return
	}
	back := d.n - d.stored
	d.block.mu.Lock()
	defer d.block.mu.Unlock()
	back = min(back, d.block.used)
	d.block.used -= back
	if !d.block.res.Unlimited {
		d.block.left += back
	}
}

// safety: the operator's team is never counted, and neither is a service
// without an archive, which has no free tier; a controller that cannot
// count answers 503.
func (s *Server) reserveLogBytes(w http.ResponseWriter, r *http.Request, team, runID string, n int64) (*logDraw, bool) {
	if s.archive == nil || s.counter == nil || storagequota.Exempt(team) {
		return &logDraw{}, true
	}
	auth, err := extractCredential(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return nil, false
	}
	for {
		b := s.logBlocks.get(logBlockKey{team: team, run: runID})
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			continue
		}
		draw, err := s.drawLocked(r.Context(), b, auth, n)
		b.mu.Unlock()
		if err != nil {
			s.writeQuotaRefusal(w, err)
			return nil, false
		}
		return draw, true
	}
}

func (s *Server) drawLocked(ctx context.Context, b *logBlock, auth string, n int64) (*logDraw, error) {
	b.auth = auth
	if b.take(n) {
		return &logDraw{block: b, n: n}, nil
	}
	want := max(n, LogBlockBytes)
	var next storagequota.Reservation
	var err error
	if b.held() {
		next, err = s.counter.Renew(ctx, auth, b.res, b.used, want)
		if err == nil || !errors.Is(err, storagequota.ErrUnavailable) {
			b.adopt(storagequota.Reservation{})
		}
	} else {
		next, err = s.counter.Reserve(ctx, auth, b.key.team, storagequota.KindLogs, want, true)
	}
	if err != nil {
		return nil, err
	}
	b.adopt(next)
	if b.take(n) {
		return &logDraw{block: b, n: n}, nil
	}
	return nil, &storagequota.QuotaError{Message: fmt.Sprintf(
		"free storage allowance exceeded: team %s has %d bytes of its log share left and this append stores %d; "+
			"add credits to store more", b.key.team, b.left, n)}
}

// safety: a run that drew nothing since the last settle, or every run when
// final is set, commits and gives the rest of its block back, so an ended run
// holds no block for more than two intervals.
func (s *Server) settleLogBlocks(ctx context.Context, final bool) {
	for _, b := range s.logBlocks.all() {
		b.mu.Lock()
		if err := s.settleLocked(ctx, b, final); err != nil {
			s.logger.Error("logs storage count", "team", b.key.team, "run", b.key.run, "err", err)
		}
		b.mu.Unlock()
	}
}

func (s *Server) settleLocked(ctx context.Context, b *logBlock, final bool) error {
	defer func() { b.active = false }()
	// safety: a block granted while the controller could not answer carries
	// no reservation, so it is dropped at the next settle and the run asks again.
	if final || !b.active || b.res.ID == "" {
		if err := s.counter.Commit(ctx, b.auth, b.res, b.used); err != nil {
			return err
		}
		b.closed = true
		s.logBlocks.drop(b)
		return nil
	}
	if b.used == 0 {
		return nil
	}
	next, err := s.counter.Renew(ctx, b.auth, b.res, b.used, LogBlockBytes)
	if err != nil && errors.Is(err, storagequota.ErrUnavailable) {
		return err
	}
	b.adopt(next)
	var quota *storagequota.QuotaError
	if errors.As(err, &quota) {
		return nil
	}
	return err
}

func (s *Server) startLogBlockSettle(ctx context.Context) {
	if s.archive == nil || s.counter == nil {
		return
	}
	go func() {
		t := time.NewTicker(LogBlockSettleEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				s.settleLogBlocks(sctx, true)
				cancel()
				return
			case <-t.C:
				s.settleLogBlocks(ctx, false)
			}
		}
	}()
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
