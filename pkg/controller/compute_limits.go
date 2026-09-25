package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ComputeLimitRefusedCode is the machine-readable code on a 429 from work a
// compute guard refused.
const ComputeLimitRefusedCode = "compute_limit"

type computeLimitsJSON struct {
	Limits  map[string]int64   `json:"limits"`
	Usage   computeUsageJSON   `json:"usage"`
	Budgets requestBudgetsJSON `json:"budgets"`
}

// safety: the budgets are process configuration rather than stored guards, so
// they are reported beside the guards and never set through this route.
type requestBudgetsJSON struct {
	ClaimsPerRunnerMinute     int64 `json:"claims_per_runner_minute"`
	HeartbeatsPerRunnerMinute int64 `json:"heartbeats_per_runner_minute"`
	IdleClaimPollSeconds      int64 `json:"idle_claim_poll_seconds"`
	IdleClaimPollEnforced     bool  `json:"idle_claim_poll_enforced"`
	MaxLogStreamsPerPrincipal int64 `json:"max_log_streams_per_principal"`
	MaxDownloadsPerPrincipal  int64 `json:"max_downloads_per_principal"`
	RequestsPerTokenMinute    int64 `json:"requests_per_token_minute"`
	RequestsPerMinuteAlarm    int64 `json:"requests_per_minute_alarm"`
}

type computeUsageJSON struct {
	Runners            *int64           `json:"runners,omitempty"`
	ByPrincipal        map[string]int64 `json:"by_principal,omitempty"`
	AlarmReached       *bool            `json:"alarm_reached,omitempty"`
	DerivedRunnerCap   int64            `json:"derived_runner_cap,omitempty"`
	RecentPaidMicro    int64            `json:"recent_paid_micro"`
	ScaleWindowSeconds int64            `json:"scale_window_seconds,omitempty"`
}

type setComputeLimitsReq struct {
	Limits map[string]int64 `json:"limits"`
}

func (r *setComputeLimitsReq) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Limits map[string]*int64 `json:"limits"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return err
	}
	r.Limits = make(map[string]int64, len(wire.Limits))
	for name, value := range wire.Limits {
		if value == nil {
			return fmt.Errorf("%s must not be null", name)
		}
		r.Limits[name] = *value
	}
	return nil
}

func (s *Server) handleComputeLimitsShow(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requestTenant(w, r); !ok {
		return
	}
	out, err := s.computeLimitsView(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleComputeLimitsSet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requestTenant(w, r); !ok {
		return
	}
	var req setComputeLimitsReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limits, err := s.store.SetComputeLimits(r.Context(), req.Limits)
	if err != nil {
		if errors.Is(err, store.ErrInvalidComputeLimitSetting) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("compute guards set", "count", len(req.Limits))
	out, err := s.computeLimitsViewWith(r, limits)
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
	return s.computeLimitsViewWith(r, limits)
}

func (s *Server) computeLimitsViewWith(r *http.Request, limits store.ComputeLimits) (computeLimitsJSON, error) {
	out := computeLimitsJSON{
		Limits:  make(map[string]int64, len(store.ComputeLimitNames())),
		Budgets: s.requestBudgetsView(),
	}
	// safety: global runner activity and per-principal counts reveal other
	// teams' work, so only the operator reads them.
	if isAdmin(r) {
		usage, err := s.store.ComputeUsage(r.Context())
		if err != nil {
			return computeLimitsJSON{}, err
		}
		out.Usage.Runners = &usage.Runners
		out.Usage.ByPrincipal = usage.ByPrincipal
		out.Usage.AlarmReached = &usage.AlarmReached
	}
	for _, name := range store.ComputeLimitNames() {
		v, _ := limits.Value(name)
		out.Limits[name] = v
	}
	if limits.ConcurrentRunners > 0 {
		team, err := requestTeam(r)
		if err != nil {
			return computeLimitsJSON{}, err
		}
		derived, err := s.store.RunnerCapFor(r.Context(), team, time.Now())
		if err != nil {
			return computeLimitsJSON{}, err
		}
		out.Usage.DerivedRunnerCap = derived.Cap
		out.Usage.RecentPaidMicro = derived.RecentPaidMicro
		if limits.RunnerScaleStepCredits > 0 {
			out.Usage.ScaleWindowSeconds = int64(store.RunnerScaleWindow.Seconds())
		}
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

// ComputeLimitRetryAfterSeconds is the Retry-After a guard refusal carries. A
// guard clears when work finishes rather than on a schedule, so it names how
// often a caller should ask again.
const ComputeLimitRetryAfterSeconds = 5

// FreeRunLimitRetryAfterSeconds is the Retry-After a refusal past a free
// team's daily run cap carries: the oldest run in the window ages out within
// a day, so an hour is a fair time to ask again.
const FreeRunLimitRetryAfterSeconds = 3600

// safety: a refusal with no run of its own reaches the operator through the
// log, because there is no run to record it against.
func (s *Server) writeComputeLimitRefusal(
	w http.ResponseWriter, r *http.Request, runID, nodeID string, err error,
) bool {
	switch {
	case errors.Is(err, store.ErrFreeStoragePaused):
		writeError(w, http.StatusPaymentRequired, err)
		return true
	case errors.Is(err, store.ErrFreeRunLimit):
		w.Header().Set("Retry-After", strconv.Itoa(FreeRunLimitRetryAfterSeconds))
		writeError(w, http.StatusTooManyRequests, err)
		return true
	}
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
	w.Header().Set("Retry-After", strconv.Itoa(ComputeLimitRetryAfterSeconds))
	writeJSON(w, http.StatusTooManyRequests, computeLimitRefusalJSON{
		Error: refused.Error(), Code: ComputeLimitRefusedCode,
		Limit: refused.Limit, Cap: refused.Cap, Observed: refused.Observed, Scope: refused.Scope,
	})
	return true
}

// safety: the poller asks twice a second, so the refusal is recorded once
// against a run the refused principal owns; recording it on a stranger's run
// would name that principal in a status its owner reads.
func (s *Server) writeClaimComputeLimitRefusal(w http.ResponseWriter, r *http.Request, err error) bool {
	var refused *store.ComputeLimitError
	if !errors.As(err, &refused) {
		return false
	}
	runID, nodeID, lookupErr := s.store.OldestWaitingReadyNodeForPrincipal(r.Context(), refused.Principal)
	if lookupErr != nil {
		s.logger.Warn("finding the run to record a compute-limit refusal against failed",
			"principal", refused.Principal, "err", lookupErr)
		runID, nodeID = "", ""
	}
	return s.writeComputeLimitRefusal(w, r, runID, nodeID, err)
}

// safety: the alarm exists to be heard once, so the crossing is logged on the
// transition rather than on every claim that stays above it.
func (s *Server) noteRunnerAlarm(r *http.Request) {
	runners, alarm, err := s.store.ComputeAlarmState(r.Context())
	if err != nil {
		s.logger.Warn("reading the cloud runner alarm failed", "err", err)
		return
	}
	if alarm <= 0 {
		return
	}
	reached := runners >= alarm
	s.computeAlarmMu.Lock()
	was := s.computeAlarmOn
	s.computeAlarmOn = reached
	s.computeAlarmMu.Unlock()
	switch {
	case reached && !was:
		s.logger.Warn("cloud runners reached the alarm count", "runners", runners, "alarm", alarm)
	case was && !reached:
		s.logger.Info("cloud runners fell back below the alarm", "runners", runners, "alarm", alarm)
	}
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
func (s *Server) stopForWallClockLimit(r *http.Request, runID, nodeID, prefix string) (stop bool) {
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
	limits, err := s.store.ComputeLimits(r.Context())
	if err != nil {
		return err
	}
	return crons.RefuseBelowMinInterval(expr, limits.CronSeconds)
}

func (s *Server) requestBudgetsView() requestBudgetsJSON {
	egressState := s.egress.State()
	return requestBudgetsJSON{
		ClaimsPerRunnerMinute:     int64(s.requestBudget.values().ClaimsPerMinute),
		HeartbeatsPerRunnerMinute: int64(s.requestBudget.values().HeartbeatsPerMinute),
		IdleClaimPollSeconds:      int64(s.idleClaimPoll / time.Second),
		IdleClaimPollEnforced:     s.idlePolls != nil,
		MaxLogStreamsPerPrincipal: int64(egressState.MaxStreamsPerPrincipal),
		MaxDownloadsPerPrincipal:  int64(egressState.MaxDownloadsPerPrincipal),
		RequestsPerTokenMinute:    int64(s.tokenBudget.values().PerTokenMinute),
		RequestsPerMinuteAlarm:    int64(s.tokenBudget.values().AlarmPerMinute),
	}
}
