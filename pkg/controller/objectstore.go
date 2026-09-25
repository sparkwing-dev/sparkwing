package controller

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// ObjectStoreBreakerResponse reports the process-wide object-store
// request budget, one entry per request class.
type ObjectStoreBreakerResponse struct {
	// Enabled is false when the process runs with the breaker switched
	// off, in which case requests are counted but never refused.
	Enabled bool `json:"enabled"`
	// Reset says what clears a tripped class without an operator:
	// "day" when the day window rolls, "manual" never.
	Reset string `json:"reset"`
	// Tripped is true while any class refuses requests.
	Tripped bool `json:"tripped"`
	// Classes carries the per-class counters and trip state.
	Classes []objectguard.ClassState `json:"classes"`
	// Ceiling is the bucket-size freeze: how much the bucket holds,
	// the ceilings it is held to, and whether writes are frozen.
	Ceiling objectguard.CeilingState `json:"ceiling"`
	// Cleared lists the classes an operator reset in this call.
	Cleared []string `json:"cleared,omitempty"`
	// Thawed is true when this call cleared a bucket-ceiling freeze.
	Thawed bool `json:"thawed,omitempty"`
}

func objectStoreBreakerState(l *objectguard.Limiter) ObjectStoreBreakerResponse {
	state := l.State()
	return ObjectStoreBreakerResponse{
		Enabled: state.Enabled,
		Reset:   string(state.Reset),
		Tripped: state.Tripped,
		Classes: state.Classes,
		Ceiling: state.Ceiling,
	}
}

func (s *Server) handleObjectStoreBreaker(w http.ResponseWriter, _ *http.Request) {
	limiter, err := objectguard.Shared()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, objectStoreBreakerState(limiter))
}

func (s *Server) handleResetObjectStoreBreaker(w http.ResponseWriter, _ *http.Request) {
	limiter, err := objectguard.Shared()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	resp := objectStoreBreakerState(limiter)
	for _, c := range limiter.Reset() {
		resp.Cleared = append(resp.Cleared, string(c))
	}
	thawed, err := limiter.Ceiling().Thaw()
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	resp.Thawed = thawed
	resp.Ceiling = limiter.Ceiling().State()
	writeJSON(w, http.StatusOK, resp)
}

// safety: the health route answers without a token, so the summary says which
// classes refuse and never what their limits are, and a failed measurement is
// named here while its error goes to the log and the admin breaker route.
func objectStoreHealth(bucket bool) (map[string]any, []string) {
	limiter, err := objectguard.Shared()
	if err != nil {
		return map[string]any{"tripped": false},
			[]string{"object-store budget unavailable"}
	}
	state := limiter.State()
	summary := map[string]any{"tripped": state.Tripped, "enabled": state.Enabled}
	var problems []string
	var tripped []string
	for _, c := range state.Classes {
		if !c.Tripped {
			continue
		}
		tripped = append(tripped, string(c.Class))
		problems = append(problems, fmt.Sprintf(
			"object-store %s budget: the per-%s limit is spent and requests of this class are refused",
			c.Class, c.TrippedWindow))
	}
	if len(tripped) > 0 {
		summary["tripped_classes"] = tripped
	}

	ceiling := state.Ceiling
	if ceiling.Enforced || bucket {
		// safety: the totals and the ceilings stay on the admin-scoped breaker route,
		// so an anonymous caller learns that writes are frozen and not how large the bucket is.
		summary["ceiling"] = map[string]any{
			"enforced":               ceiling.Enforced,
			"frozen":                 ceiling.Frozen,
			"warning":                ceiling.Warning,
			"measurement_incomplete": ceiling.Incomplete,
			"measured_at":            ceiling.ReconciledAt,
		}
		switch {
		case ceiling.Frozen:
			problems = append(problems, fmt.Sprintf(
				"object-store bucket ceiling: the bucket is over its %s ceiling and object writes are frozen", ceiling.FrozenReason))
		case ceiling.Warning:
			problems = append(problems, "object-store bucket ceiling: the bucket is past its warning mark")
		}
		if ceiling.Incomplete {
			problems = append(problems,
				"object-store bucket: the last measurement failed or did not finish, so the total is the running count; "+
					"the controller log and the admin object-store route name the error")
		}
	}

	stalls := objectguard.Stalls()
	if len(stalls) > 0 {
		summary["stalled"] = true
		problems = append(problems, "object-store replay stalled")
	}
	return summary, problems
}

// safety: a backend that cannot total itself yields a partial measurement rather
// than a zero one, so an unmeasurable store never reads as an empty bucket.
func (s *Server) bucketUsage(ctx context.Context) (objectguard.Usage, error) {
	measured := s.bucketUsageStore
	if measured == nil {
		measured = s.artifactStore
	}
	if measured == nil {
		return objectguard.Usage{Partial: true}, nil
	}
	u, ok, err := storage.Usage(ctx, measured)
	if err != nil {
		return objectguard.Usage{}, err
	}
	if !ok {
		return objectguard.Usage{Partial: true}, nil
	}
	return objectguard.Usage{
		Bytes:      u.Bytes,
		Objects:    u.Objects,
		ObservedAt: u.ObservedAt,
		Partial:    u.Partial,
	}, nil
}

// perf: one replica per window pays for the listing, because a bucket total is
// the same answer whoever asks and each pass is billed per thousand keys.
func (s *Server) measureBucketLeased(ctx context.Context, ceiling *objectguard.Ceiling, window time.Duration) (bool, error) {
	measure := func(ctx context.Context) error {
		// safety: the measurement gets less than the window it runs on, so a bucket
		// that cannot be walked in time reports itself partial instead of overlapping
		// the next one.
		if window > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, window/2)
			defer cancel()
		}
		return ceiling.Measure(ctx, s.bucketUsage)
	}
	if s.store == nil || window <= 0 {
		return true, measure(ctx)
	}
	return s.store.RunBucketMeasureLeased(ctx, s.measureHolder(), window, window, measure)
}

func (s *Server) measureHolder() string {
	if s.cronHolder != "" {
		return s.cronHolder
	}
	return "controller"
}

func (s *Server) bucketMeasured() bool {
	return s.bucketUsageStore != nil || s.artifactStore != nil
}

// perf: an install with neither a bucket nor a ceiling returns before the
// first listing, so it never pays to enumerate anything.
func (s *Server) runBucketCeiling(ctx context.Context) {
	limiter, err := objectguard.Shared()
	if err != nil {
		s.logger.Warn("object-store bucket ceiling", "err", err)
		return
	}
	ceiling := limiter.Ceiling()
	if !ceiling.Enforced() && !s.bucketMeasured() {
		return
	}
	interval := ceiling.Reconcile()
	measure := func() {
		ran, err := s.measureBucketLeased(ctx, ceiling, interval)
		if err != nil {
			s.logger.Error("object-store bucket ceiling", "op", "measure bucket", "err", err)
			return
		}
		if !ran {
			return
		}
		state := ceiling.State()
		s.logger.Info("object-store bucket measured",
			"bytes", state.Bytes, "objects", state.Objects, "frozen", state.Frozen)
	}
	measure()
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			measure()
		}
	}
}
