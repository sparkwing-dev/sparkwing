package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ComputeLimitRefusedCode is the machine-readable code on a 429 from work a
// compute guard refused.
const ComputeLimitRefusedCode = "compute_limit"

type computeLimitsJSON struct {
	Limits map[string]int64 `json:"limits"`
	Usage  computeUsageJSON `json:"usage"`
}

type computeUsageJSON struct {
	Runners      int64            `json:"runners"`
	ByPrincipal  map[string]int64 `json:"by_principal,omitempty"`
	AlarmReached bool             `json:"alarm_reached"`
}

type setComputeLimitsReq struct {
	Limits map[string]int64 `json:"limits"`
}

func (s *Server) handleComputeLimitsShow(w http.ResponseWriter, r *http.Request) {
	out, err := s.computeLimitsView(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleComputeLimitsSet(w http.ResponseWriter, r *http.Request) {
	var req setComputeLimitsReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Limits) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("limits must name at least one guard"))
		return
	}
	for name, value := range req.Limits {
		if !store.ValidComputeLimit(name) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("unknown guard %q", name))
			return
		}
		if value < 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("%s must not be negative", name))
			return
		}
	}
	for name, value := range req.Limits {
		if err := s.store.SetComputeLimit(r.Context(), name, value); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.logger.Info("compute guard set", "limit", name, "value", value)
	}
	out, err := s.computeLimitsView(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) computeLimitsView(r *http.Request) (computeLimitsJSON, error) {
	limits, err := s.store.ComputeLimits(r.Context())
	if err != nil {
		return computeLimitsJSON{}, err
	}
	usage, err := s.store.ComputeUsage(r.Context())
	if err != nil {
		return computeLimitsJSON{}, err
	}
	out := computeLimitsJSON{
		Limits: make(map[string]int64, len(store.ComputeLimitNames())),
		Usage: computeUsageJSON{
			Runners:      usage.Runners,
			ByPrincipal:  usage.ByPrincipal,
			AlarmReached: usage.AlarmReached,
		},
	}
	for _, name := range store.ComputeLimitNames() {
		v, _ := limits.Value(name)
		out.Limits[name] = v
	}
	return out, nil
}

// safety: a runner tells this apart from a transport failure and keeps polling
// rather than retrying the request.
type computeLimitRefusalJSON struct {
	Error    string `json:"error"`
	Code     string `json:"code"`
	Limit    string `json:"limit"`
	Cap      int64  `json:"cap"`
	Observed int64  `json:"observed"`
	Scope    string `json:"scope,omitempty"`
}

// safety: a refusal with no run of its own reaches the operator through the
// log, because there is no run to record it against.
func (s *Server) writeComputeLimitRefusal(
	w http.ResponseWriter, r *http.Request, runID, nodeID string, err error,
) bool {
	var refused *store.ComputeLimitError
	if !errors.As(err, &refused) {
		return false
	}
	if runID != "" {
		s.noteComputeLimitBlocked(r, runID, nodeID, refused)
	} else {
		s.logger.Warn("refused by a compute guard",
			"limit", refused.Limit, "cap", refused.Cap,
			"observed", refused.Observed, "scope", refused.Scope)
	}
	writeJSON(w, http.StatusTooManyRequests, computeLimitRefusalJSON{
		Error: refused.Error(), Code: ComputeLimitRefusedCode,
		Limit: refused.Limit, Cap: refused.Cap, Observed: refused.Observed, Scope: refused.Scope,
	})
	return true
}

// safety: the poller asks twice a second, so the waiting run records the
// refusal once per node rather than on every poll.
func (s *Server) writeClaimComputeLimitRefusal(w http.ResponseWriter, r *http.Request, err error) bool {
	var refused *store.ComputeLimitError
	if !errors.As(err, &refused) {
		return false
	}
	runID, nodeID, lookupErr := s.store.OldestWaitingReadyNode(r.Context())
	if lookupErr != nil {
		runID = ""
	}
	return s.writeComputeLimitRefusal(w, r, runID, nodeID, err)
}

func (s *Server) noteComputeLimitBlocked(
	r *http.Request, runID, nodeID string, refused *store.ComputeLimitError,
) {
	ctx := r.Context()
	payload, err := json.Marshal(map[string]any{
		"limit": refused.Limit, "cap": refused.Cap,
		"observed": refused.Observed, "scope": refused.Scope,
	})
	if err != nil {
		return
	}
	wrote, err := s.store.AppendEventOnce(ctx, runID, nodeID, store.EventKindComputeLimitBlocked, payload)
	if err != nil {
		s.logger.Warn("recording a compute-limit refusal failed",
			"run_id", runID, "node_id", nodeID, "err", err)
		return
	}
	if wrote {
		s.logger.Warn("refused by a compute guard",
			"run_id", runID, "node_id", nodeID, "limit", refused.Limit,
			"cap", refused.Cap, "observed", refused.Observed, "scope", refused.Scope)
	}
}

// safety: a run past the wall-clock guard stops in this same request, so the
// runner learns to abandon the node rather than holding it to the lease.
func (s *Server) stopForWallClockLimit(r *http.Request, runID, nodeID string) (stop bool) {
	prefix := s.meteredTokenPrefix(r)
	if prefix == "" {
		return false
	}
	ctx := r.Context()
	now := time.Now()
	ceiling, over, err := s.store.RunExceedsWallClock(ctx, runID, now)
	if err != nil {
		s.logger.Warn("reading the wall-clock guard failed",
			"run_id", runID, "node_id", nodeID, "err", err)
		return false
	}
	if !over {
		return false
	}
	refused := &store.ComputeLimitError{
		Limit: store.ComputeLimitRunSeconds, Cap: ceiling,
		Observed: ceiling, Scope: "run " + runID,
	}
	s.noteComputeLimitBlocked(r, runID, nodeID, refused)
	if err := s.store.CancelNodeForComputeLimit(
		ctx, runID, nodeID, prefix, store.ComputeLimitRunSeconds, now); err != nil {
		s.logger.Error("cancelling a node for the wall-clock guard failed",
			"run_id", runID, "node_id", nodeID, "err", err)
		return false
	}
	s.logger.Warn("cancelled a node: the run passed the wall-clock guard",
		"run_id", runID, "node_id", nodeID, "max_run_seconds", ceiling)
	return true
}

// safety: a schedule is evaluated over its next fires rather than its text,
// so `*/1 * * * *` and `0,1,2 * * * *` are both measured at one minute.
func (s *Server) cronIntervalRefusal(r *http.Request, expr string) error {
	if expr == "" {
		return nil
	}
	limits, err := s.store.ComputeLimits(r.Context())
	if err != nil {
		return err
	}
	if limits.CronSeconds <= 0 {
		return nil
	}
	shortest, ok := shortestCronInterval(expr)
	if !ok {
		return nil
	}
	if shortest >= limits.CronSeconds {
		return nil
	}
	return &store.ComputeLimitError{
		Limit: store.ComputeLimitCronSeconds, Cap: limits.CronSeconds,
		Observed: shortest, Scope: "schedule " + expr,
	}
}

// safety: ten fires cover the repeating step of every cadence a minute field
// can express, so the shortest gap it measures is the schedule's own.
const cronIntervalSamples = 10

func shortestCronInterval(expr string) (int64, bool) {
	sched, err := cronspec.Parse(expr)
	if err != nil {
		return 0, false
	}
	fires := sched.Upcoming(time.Now().UTC(), time.UTC, cronIntervalSamples)
	if len(fires) < 2 {
		return 0, false
	}
	shortest := int64(0)
	for i := 1; i < len(fires); i++ {
		gap := int64(fires[i].Sub(fires[i-1]).Seconds())
		if shortest == 0 || gap < shortest {
			shortest = gap
		}
	}
	return shortest, shortest > 0
}
