package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const standingQueueLimit = 100

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func standingRequestBody(r store.StandingRequest) controllerwire.StandingRequest {
	return controllerwire.StandingRequest{
		ID: r.ID, Team: string(r.Team), Tier: r.Tier, Message: r.Message, RequestedBy: r.RequesterEmail,
		RequestedAt: r.RequestedAt.Unix(), State: r.State, DecidedAt: unixOrZero(r.DecidedAt), DecisionReason: r.DecisionReason,
	}
}

func standingHistoryBody(h store.StandingHistory) controllerwire.StandingHistory {
	return controllerwire.StandingHistory{
		Tier: h.Tier, PaidMicro: h.PaidMicro,
		FirstPaidAt: unixOrZero(h.FirstPaidAt), LastIncidentAt: unixOrZero(h.LastIncidentAt),
	}
}

func teamStandingBody(ctx context.Context, t *store.Tenant, b store.BillingStanding, admin bool) (controllerwire.TeamStanding, error) {
	out := controllerwire.TeamStanding{Tier: b.Tier, History: standingHistoryBody(b.History), PrepayCeiling: b.PrepayCeiling}
	latest, found, err := t.LatestStandingRequest(ctx)
	if err != nil {
		return out, err
	}
	if found {
		body := standingRequestBody(latest)
		out.Request = &body
	}
	out.CanRequest = admin && (!found || latest.State != store.StandingRequestOpen)
	return out, nil
}

func (s *Server) handleTeamStandingRequest(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var req controllerwire.StandingRequestWrite
	if err := decodeEngineRequest(r, &req); err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(req.Tier), "high") {
		return &apierr.Error{Code: apierr.BadRequest, Message: "tier must be high; Trusted is reachable with payment history or prepaid funds"}
	}
	writer, err := s.billingWriter(r, a)
	if err != nil {
		return err
	}
	made, err := writer.RequestAuthorizedStanding(r.Context(), a, "High", req.Message)
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	case errors.Is(err, store.ErrStandingRequestOpen):
		return &apierr.Error{Code: apierr.Conflict, Message: err.Error()}
	case err != nil:
		return billingMutationError(err)
	}
	writeJSON(w, http.StatusCreated, standingRequestBody(made))
	return nil
}

func (s *Server) handleOperatorStandingRequests(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	ctx := r.Context()
	open, err := s.store.OpenStandingRequests(ctx, standingQueueLimit)
	if err != nil {
		return err
	}
	out := controllerwire.OperatorStandingQueue{Requests: []controllerwire.OperatorStandingRequest{}}
	for _, req := range open {
		t, err := s.consoleTenant(ctx, string(req.Team))
		if err != nil {
			return err
		}
		info, err := t.Info(ctx)
		standing, serr := t.BillingStanding(ctx, time.Now())
		credit, cerr := t.CreditState(ctx, creditsBurnWindow)
		if err := errors.Join(err, serr, cerr); err != nil {
			return err
		}
		out.Requests = append(out.Requests, controllerwire.OperatorStandingRequest{
			Request: standingRequestBody(req), DisplayName: info.DisplayName, Standing: standing.Tier,
			PrepayCeiling: standing.PrepayCeiling, BalanceMicro: credit.BalanceMicro, History: standingHistoryBody(standing.History),
		})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleOperatorDeclineStanding(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var req controllerwire.StandingDecline
	if err := decodeJSON(r, &req); err != nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	reason, err := OperatorReason(req.Reason)
	if err != nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	if req.Request = strings.TrimSpace(req.Request); req.Request == "" {
		return &apierr.Error{Code: apierr.BadRequest, Message: "request names the reviewed standing request"}
	}
	writer, err := s.consoleWriter()
	if err != nil {
		return err
	}
	declined, err := writer.ConsoleDeclineStandingRequest(r.Context(), a, req.Request, reason)
	if errors.Is(err, store.ErrStandingRequestClosed) {
		return &apierr.Error{Code: apierr.Conflict, Message: err.Error()}
	}
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, standingRequestBody(declined))
	return nil
}
