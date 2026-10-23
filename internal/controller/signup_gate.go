package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// FreeTierState is how much of the deployment's free storage is left to hand
// out: open, throttled, or closed once it is spent.
type FreeTierState string

// The free-tier states. FreeTierUnreadable is what the gate reports when the
// source failed; it waitlists new accounts as closed does.
const (
	FreeTierOpen       FreeTierState = "open"
	FreeTierThrottled  FreeTierState = "throttled"
	FreeTierClosed     FreeTierState = "closed"
	FreeTierUnreadable FreeTierState = "unreadable"
)

// FreeTierSource reports how much of the deployment's free storage is left.
// It runs on every sign-in, so it should answer from memory.
type FreeTierSource interface {
	SignUpFreeTier(ctx context.Context) (FreeTierState, error)
}

// SignUpFreeTier reports the free tier closed once every free-team slot is
// taken, and open while one is left. A slot count it cannot read is an
// error, never open, so a sign-up gate reading it fails closed.
func (s *Server) SignUpFreeTier(ctx context.Context) (FreeTierState, error) {
	taken, limit, err := s.operator.FreeSlots(ctx)
	if err != nil {
		return FreeTierUnreadable, fmt.Errorf("read free-team slots: %w", err)
	}
	if taken >= limit {
		return FreeTierClosed, nil
	}
	return FreeTierOpen, nil
}

// FreeTierFunc adapts a function to [FreeTierSource].
type FreeTierFunc func(ctx context.Context) (FreeTierState, error)

// SignUpFreeTier calls f.
func (f FreeTierFunc) SignUpFreeTier(ctx context.Context) (FreeTierState, error) { return f(ctx) }

type signUpConfig struct {
	forceWaitlist bool
	freeTier      FreeTierSource
	unwiredLogged atomic.Bool
	onApproved    func(context.Context, store.Account) error
	// safety: set while the hourly count sits above the warn threshold, so the warning fires once per
	// crossing rather than on every sign-up.
	warned atomic.Bool
}

// WithSignUpWaitlist places every new account on the waitlist when on is
// true, whatever the stored gate says. Accounts that already exist are not
// affected.
func (s *Server) WithSignUpWaitlist(on bool) *Server {
	s.identity.signup.forceWaitlist = on
	return s
}

// WithFreeTier installs the report of the deployment's free storage. While it
// reports closed, or fails, new accounts are waitlisted, because a personal
// space holds a free allowance the deployment may no longer carry.
func (s *Server) WithFreeTier(src FreeTierSource) *Server {
	s.identity.signup.freeTier = src
	return s
}

// CheckFreeTierSource logs signup.free_tier_unwired once when a multi-team
// controller has no free-tier source, in which case the gate treats the free
// tier as open. Call it at startup, after configuration.
func (s *Server) CheckFreeTierSource() {
	if s.identity.signup.freeTier != nil || !s.deployment.MultiTeam() {
		return
	}
	if s.identity.signup.unwiredLogged.Swap(true) {
		return
	}
	s.logger.Warn("signup.free_tier_unwired",
		"detail", "no free-tier source is configured, so the sign-up gate treats the free tier as open")
}

// WithSignUpApprovalNotifier runs fn for each account an operator admits from
// the waitlist, such as to email the person that they are in. A failure is
// logged and does not undo the admission.
func (s *Server) WithSignUpApprovalNotifier(fn func(context.Context, store.Account) error) *Server {
	s.identity.signup.onApproved = fn
	return s
}

// safety: a report that fails reads as unreadable, which waitlists new accounts as closed does: an
// account held back is one approval away, while one admitted keeps a free allowance for good.
func (s *Server) freeTierState(ctx context.Context) FreeTierState {
	src := s.identity.signup.freeTier
	if src == nil {
		return FreeTierOpen
	}
	state, err := src.SignUpFreeTier(ctx)
	if err != nil {
		s.logger.Warn("signup.free_tier_unreadable", "error", err.Error())
		return FreeTierUnreadable
	}
	return state
}

func (s *Server) signUpConditions(ctx context.Context) store.SignUpConditions {
	free := s.freeTierState(ctx)
	return store.SignUpConditions{
		ForceWaitlist:      s.identity.signup.forceWaitlist,
		FreeTierClosed:     free == FreeTierClosed,
		FreeTierUnreadable: free == FreeTierUnreadable,
	}
}

func (s *Server) observeSignUp(ctx context.Context, provider string, res store.SignInResult, now time.Time) {
	if !res.NewAccount {
		return
	}
	outcome, reason := "admitted", "none"
	if res.WaitlistReason != "" {
		outcome, reason = "waitlisted", res.WaitlistReason
		s.logger.Info("signup.waitlisted", "account", res.Account.ID, "provider", provider, "reason", reason)
	}
	observeSignUpOutcome(outcome, reason)
	if g := res.GateClosed; g != nil {
		observeSignUpGateClosed(g.Source)
		s.logger.Warn("signup.gate_closed", "source", g.Source, "reason", g.Reason)
	}
	g, err := s.operator.SignUpGate(ctx)
	if err != nil {
		s.logger.Warn("signup.velocity_unreadable", "error", err.Error())
		return
	}
	counts, err := s.operator.SignUpCounts(ctx)
	if err != nil {
		s.logger.Warn("signup.velocity_unreadable", "error", err.Error())
		return
	}
	s.checkSignUpVelocity(g.Limits, counts)
}

func (s *Server) checkSignUpVelocity(limits store.SignUpLimits, counts store.SignUpCounts) {
	above := limits.HourlyWarn > 0 && counts.LastHour > limits.HourlyWarn
	if !above {
		s.identity.signup.warned.Store(false)
		return
	}
	if s.identity.signup.warned.Swap(true) {
		return
	}
	observeSignUpVelocityWarning()
	s.logger.Warn("signup.velocity_warning", "last_hour", counts.LastHour, "hourly_warn", limits.HourlyWarn,
		"hourly_limit", limits.HourlyLimit, "last_day", counts.LastDay, "daily_limit", limits.DailyLimit)
}

type signUpLimitsJSON struct {
	HourlyLimit          int `json:"hourly_limit"`
	DailyLimit           int `json:"daily_limit"`
	HourlyWarn           int `json:"hourly_warn"`
	GitHubMinAccountDays int `json:"github_min_account_days"`
	FreeTeamMembers      int `json:"free_team_members"`
}

type signUpCountsJSON struct {
	Admitted   int `json:"admitted"`
	LastHour   int `json:"last_hour"`
	LastDay    int `json:"last_day"`
	Waitlisted int `json:"waitlisted"`
}

type signUpStatusJSON struct {
	AccountLimit       int              `json:"account_limit"`
	State              string           `json:"state"`
	Reasons            []string         `json:"reasons"`
	Mode               string           `json:"mode"`
	Source             string           `json:"source"`
	Reason             string           `json:"reason"`
	SetBy              string           `json:"set_by"`
	UpdatedAt          int64            `json:"updated_at"`
	DeploymentWaitlist bool             `json:"deployment_waitlist"`
	FreeTier           string           `json:"free_tier"`
	Limits             signUpLimitsJSON `json:"limits"`
	Counts             signUpCountsJSON `json:"counts"`
	VelocityWarning    bool             `json:"velocity_warning"`
}

func (s *Server) signUpStatus(ctx context.Context, now time.Time) (signUpStatusJSON, error) {
	g, err := s.operatorSettings.SignUpGate(ctx)
	if err != nil {
		return signUpStatusJSON{}, err
	}
	counts, err := s.operatorSettings.SignUpCounts(ctx)
	if err != nil {
		return signUpStatusJSON{}, err
	}
	free := s.freeTierState(ctx)
	out := signUpStatusJSON{
		State: string(store.SignUpOpen), Reasons: []string{}, AccountLimit: store.LaunchAccountLimit,
		Mode: string(g.Mode), Source: g.Source, Reason: g.Reason, SetBy: g.SetBy, UpdatedAt: g.UpdatedAt.Unix(),
		DeploymentWaitlist: s.identity.signup.forceWaitlist, FreeTier: string(free),
		Limits: signUpLimitsJSON{
			HourlyLimit: g.Limits.HourlyLimit, DailyLimit: g.Limits.DailyLimit,
			HourlyWarn: g.Limits.HourlyWarn, GitHubMinAccountDays: g.Limits.GitHubMinAccountDays,
			FreeTeamMembers: g.Limits.FreeTeamMembers,
		},
		Counts:          signUpCountsJSON{LastHour: counts.LastHour, LastDay: counts.LastDay, Waitlisted: counts.Waitlisted, Admitted: counts.Admitted},
		VelocityWarning: g.Limits.HourlyWarn > 0 && counts.LastHour > g.Limits.HourlyWarn,
	}
	if counts.Admitted >= store.LaunchAccountLimit {
		out.Reasons = append(out.Reasons, store.WaitlistReasonLaunchCapacity)
	}
	if out.DeploymentWaitlist {
		out.Reasons = append(out.Reasons, store.WaitlistReasonDeployment)
	}
	if g.Mode == store.SignUpWaitlist {
		out.Reasons = append(out.Reasons, g.Source)
	}
	switch free {
	case FreeTierClosed:
		out.Reasons = append(out.Reasons, store.WaitlistReasonFreeTier)
	case FreeTierUnreadable:
		out.Reasons = append(out.Reasons, store.WaitlistReasonFreeTierUnreadable)
	}
	if len(out.Reasons) > 0 {
		out.State = string(store.SignUpWaitlist)
	}
	return out, nil
}

func (s *Server) handleSignUpStatus(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	out, err := s.signUpStatus(r.Context(), time.Now().UTC())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleSetSignUp(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	var req store.SignUpSettingsChange
	if err := decodeJSON(r, &req); err != nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: "invalid signup settings"}
	}
	if req.Mode == nil && !req.LimitsChanged() {
		return &apierr.Error{Code: apierr.BadRequest, Message: "name a mode or at least one limit to change"}
	}
	ctx, now := r.Context(), time.Now().UTC()
	if req.Mode != nil {
		if _, err := store.ParseSignUpMode(*req.Mode); err != nil {
			return &apierr.Error{Code: apierr.BadRequest, Message: "invalid signup mode"}
		}
	}
	writer, err := s.settingsWriter()
	if err != nil {
		return err
	}
	if err := writer.WriteSignUpSettings(ctx, authorized, req); err != nil {
		return operatorSettingsError(err)
	}
	out, err := s.signUpStatus(ctx, now)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type waitlistedJSON struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	Reason       string `json:"reason"`
	Provider     string `json:"provider"`
	CreatedAt    int64  `json:"created_at"`
	WaitlistedAt int64  `json:"waitlisted_at"`
}

// perf: bounds one read of the list and one bulk approval, since each approval creates a team.
const maxWaitlistPage = 1000

func waitlistBody(waiting []store.WaitlistedAccount) []waitlistedJSON {
	out := make([]waitlistedJSON, 0, len(waiting))
	for _, a := range waiting {
		out = append(out, waitlistedJSON{
			ID: a.ID, Email: a.Email, Name: a.Name, Reason: a.Reason, Provider: a.Provider,
			CreatedAt: a.CreatedAt.Unix(), WaitlistedAt: a.WaitlistedAt.Unix(),
		})
	}
	return out
}

type approveWaitlistReq struct {
	AccountIDs []string `json:"account_ids"`
	Oldest     int      `json:"oldest"`
}

type approvedJSON struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	ActiveTeam string `json:"active_team"`
}

func (s *Server) handleApproveWaitlist(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	var req approveWaitlistReq
	if err := decodeJSON(r, &req); err != nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	if (len(req.AccountIDs) > 0) == (req.Oldest > 0) {
		return &apierr.Error{Code: apierr.BadRequest, Message: "name either account_ids or oldest"}
	}
	if len(req.AccountIDs) > maxWaitlistPage || req.Oldest > maxWaitlistPage {
		return &apierr.Error{Code: apierr.BadRequest, Message: fmt.Sprintf("approve at most %d accounts at a time", maxWaitlistPage)}
	}
	if req.Oldest < 0 {
		return invalidEngineBody("oldest must be positive")
	}
	key, err := controlIdempotencyKey(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	actor := authorized.Principal.Capability.Subject.ID
	writer, err := s.consoleWriter()
	if err != nil {
		return err
	}
	approved, err := writer.ConsoleApproveWaitlist(ctx, authorized, req.AccountIDs, req.Oldest, key)
	if err != nil {
		return engineLookupError(err)
	}
	out := make([]approvedJSON, 0, len(approved))
	for _, a := range approved {
		s.logger.Info("signup.approved", "account", a.ID, "by", actor, "team", string(a.ActiveTeam))
		s.notifyApproved(ctx, a)
		out = append(out, approvedJSON{ID: a.ID, Email: a.Email, ActiveTeam: string(a.ActiveTeam)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"approved": out})
	return nil
}

func (s *Server) notifyApproved(ctx context.Context, a store.Account) {
	fn := s.identity.signup.onApproved
	if fn == nil {
		s.logger.Info("signup.approval_not_sent", "account", a.ID, "reason", "no approval notifier is configured")
		return
	}
	if err := fn(ctx, a); err != nil {
		s.logger.Warn("signup.approval_notify_failed", "account", a.ID, "error", strings.TrimSpace(err.Error()))
	}
}
