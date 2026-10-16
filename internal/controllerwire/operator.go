package controllerwire

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// OperatorLimits reports configured guards and observed request budgets.
type OperatorLimits struct {
	Limits  map[string]int64 `json:"limits"`
	Usage   OperatorUsage    `json:"usage"`
	Budgets OperatorBudgets  `json:"budgets"`
}

// OperatorUsage preserves unavailable observations separately from zero.
type OperatorUsage struct {
	Runners      *int64           `json:"runners,omitempty"`
	ByPrincipal  map[string]int64 `json:"by_principal,omitempty"`
	AlarmReached *bool            `json:"alarm_reached,omitempty"`
}

// OperatorBudgets reports the controller's configured request protection.
type OperatorBudgets struct {
	ClaimsPerRunnerMinute     int64 `json:"claims_per_runner_minute"`
	HeartbeatsPerRunnerMinute int64 `json:"heartbeats_per_runner_minute"`
	IdleClaimPollSeconds      int64 `json:"idle_claim_poll_seconds"`
	IdleClaimPollEnforced     bool  `json:"idle_claim_poll_enforced"`
	MaxLogStreamsPerPrincipal int64 `json:"max_log_streams_per_principal"`
	MaxDownloadsPerPrincipal  int64 `json:"max_downloads_per_principal"`
	RequestsPerTokenMinute    int64 `json:"requests_per_token_minute"`
	RequestsPerMinuteAlarm    int64 `json:"requests_per_minute_alarm"`
}

// OperatorLimitsWrite preserves explicit zero; the controller validates supported names.
type OperatorLimitsWrite struct {
	Limits map[string]int64 `json:"limits"`
}

// Validate refuses an empty mutation, blank names and negative guards.
func (w OperatorLimitsWrite) Validate() error {
	if len(w.Limits) == 0 {
		return fmt.Errorf("at least one operational guard is required")
	}
	for name, value := range w.Limits {
		if name == "" || strings.TrimSpace(name) != name || value < 0 {
			return fmt.Errorf("guard names must be nonempty and values must be nonnegative integers")
		}
	}
	return nil
}

// ObjectStoreClassState reports one request class's counters and trip state.
type ObjectStoreClassState struct {
	Class         string    `json:"class"`
	PerMinute     int       `json:"per_minute"`
	PerDay        int       `json:"per_day"`
	MinuteUsed    int       `json:"minute_used"`
	DayUsed       int       `json:"day_used"`
	Allowed       uint64    `json:"allowed_total"`
	Refused       uint64    `json:"refused_total"`
	Trips         uint64    `json:"trips_total"`
	Tripped       bool      `json:"tripped"`
	TrippedAt     time.Time `json:"tripped_at,omitzero"`
	TrippedWindow string    `json:"tripped_window,omitempty"`
}

// ObjectStoreCeiling retains measured totals, freezes and incomplete observations.
type ObjectStoreCeiling struct {
	Enforced     bool      `json:"enforced"`
	MaxBytes     int64     `json:"max_bytes"`
	MaxObjects   int64     `json:"max_objects"`
	WarnBytes    int64     `json:"warn_bytes"`
	WarnObjects  int64     `json:"warn_objects"`
	Bytes        int64     `json:"bytes"`
	Objects      int64     `json:"objects"`
	CountedAt    time.Time `json:"counted_at,omitzero"`
	ReconciledAt time.Time `json:"reconciled_at,omitzero"`
	Reconcile    string    `json:"reconcile_interval,omitempty"`
	Warning      bool      `json:"warning"`
	Frozen       bool      `json:"frozen"`
	Thawed       bool      `json:"thawed"`
	FrozenAt     time.Time `json:"frozen_at,omitzero"`
	FrozenReason string    `json:"frozen_reason,omitempty"`
	Freezes      uint64    `json:"freezes_total"`
	Refused      uint64    `json:"refused_total"`
	Incomplete   bool      `json:"measurement_incomplete"`
	MeasureError string    `json:"measure_error,omitempty"`
}

// ObjectStoreBreaker reports the controller's request and bucket budgets.
type ObjectStoreBreaker struct {
	Enabled bool                    `json:"enabled"`
	Reset   string                  `json:"reset"`
	Tripped bool                    `json:"tripped"`
	Classes []ObjectStoreClassState `json:"classes"`
	Ceiling ObjectStoreCeiling      `json:"ceiling"`
	Cleared []string                `json:"cleared,omitempty"`
	Thawed  bool                    `json:"thawed,omitempty"`
}

// OperatorManager manages team creation, operational guards and storage budgets under operator authority.
type OperatorManager interface {
	CreateOperatorTeam(context.Context, OperatorTeamCreate) (OperatorTeamCreated, error)
	ReadOperatorLimits(context.Context) (OperatorLimits, error)
	WriteOperatorLimits(context.Context, OperatorLimitsWrite) (OperatorLimits, error)
	ReadObjectStoreBreaker(context.Context) (ObjectStoreBreaker, error)
	ResetObjectStoreBreaker(context.Context, string) (ObjectStoreBreaker, error)
}

// OperatorTeamCreate names a new team for operator-managed onboarding.
type OperatorTeamCreate struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name,omitempty"`
}

// OperatorTeamCreated carries the first administrator's one-time presentation.
type OperatorTeamCreated struct {
	Team       string             `json:"team"`
	AccountID  string             `json:"account_id"`
	Capability string             `json:"capability"`
	Prefix     string             `json:"prefix"`
	Grants     []capability.Grant `json:"grants"`
}
