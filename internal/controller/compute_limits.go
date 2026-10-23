package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// ComputeLimitRefusedCode is details.reason on the rate_limited refusal of work
// a compute guard refused.
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
	Runners      *int64           `json:"runners,omitempty"`
	ByPrincipal  map[string]int64 `json:"by_principal,omitempty"`
	AlarmReached *bool            `json:"alarm_reached,omitempty"`
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

func (s *Server) handleComputeLimitsShow(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	limits, err := s.operatorSettings.ComputeLimits(r.Context())
	if err != nil {
		return err
	}
	out, err := s.computeLimitsViewWith(r, limits)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleComputeLimitsSet(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	var req setComputeLimitsReq
	if err := decodeJSON(r, &req); err != nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: "invalid limits request"}
	}
	writer, err := s.settingsWriter()
	if err != nil {
		return err
	}
	limits, err := writer.WriteComputeLimits(r.Context(), authorized, req.Limits)
	if errors.Is(err, store.ErrInvalidComputeLimitSetting) {
		return &apierr.Error{Code: apierr.BadRequest, Message: "limits must name valid guards with nonnegative values"}
	}
	if err != nil {
		return err
	}
	s.logger.Info("compute guards set", "count", len(req.Limits))
	out, err := s.computeLimitsViewWith(r, limits)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) computeLimitsViewWith(r *http.Request, limits store.ComputeLimits) (computeLimitsJSON, error) {
	usage, err := s.operatorSettings.ComputeUsage(r.Context())
	if err != nil {
		return computeLimitsJSON{}, err
	}
	out := computeLimitsJSON{
		Limits:  make(map[string]int64, len(store.ComputeLimitNames())),
		Budgets: s.requestBudgetsView(),
		Usage:   computeUsageJSON{Runners: &usage.Runners, ByPrincipal: usage.ByPrincipal, AlarmReached: &usage.AlarmReached},
	}
	for _, name := range store.ComputeLimitNames() {
		v, _ := limits.Value(name)
		out.Limits[name] = v
	}
	return out, nil
}

// ComputeLimitRetryAfterSeconds is the Retry-After a guard refusal carries. A
// guard clears when work finishes rather than on a schedule, so it names how
// often a caller should ask again.
const ComputeLimitRetryAfterSeconds = 5

// safety: a refusal with no run of its own reaches the operator through the
// log, because there is no run to record it against.
func (s *Server) writeComputeLimitRefusal(
	w http.ResponseWriter, r *http.Request, err error,
) bool {
	switch {
	case errors.Is(err, store.ErrFreeStoragePaused):
		writeError(w, http.StatusPaymentRequired, err)
		return true
	}
	var refused *store.ComputeLimitError
	if !errors.As(err, &refused) {
		return false
	}
	s.logger.Warn("refused by a compute guard",
		"limit", refused.Limit, "cap", refused.Cap,
		"observed", refused.Observed, "scope", refused.Scope)
	details := map[string]any{
		"reason": ComputeLimitRefusedCode, "limit": refused.Limit, "cap": refused.Cap, "observed": refused.Observed,
		"retry_after_ms": ComputeLimitRetryAfterSeconds * 1000,
	}
	if refused.Scope != "" {
		details["scope"] = refused.Scope
	}
	routepolicy.WritePolicyError(w, &apierr.Error{Code: apierr.RateLimited, Message: refused.Error(), Details: details})
	return true
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
