package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// WithOperatorAccounts names the accounts, by account id, whose signed-in
// browser sessions may use the operator console routes under
// /api/v1/operator. None is named by default, which leaves the console off.
func (s *Server) WithOperatorAccounts(ids []string) *Server {
	set := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = true
		}
	}
	s.operators.Store(&set)
	return s
}

// safety: only a browser session of a listed account passes. No bearer token
// does, an admin token included, so a console action needs the operator's own
// sign-in and never a credential that automation can hold.
func (s *Server) requireOperator(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		set := s.operators.Load()
		if !ok || p.session == "" || p.AccountID == "" || set == nil || !(*set)[p.AccountID] {
			writeAuthError(w, http.StatusForbidden, authErrorBody{
				Code: "forbidden", Message: "the operator console needs the operator's own signed-in account",
			})
			return
		}
		next(w, r)
	})
}

func operatorActor(r *http.Request) string {
	p, _ := PrincipalFromContext(r.Context())
	return p.Name
}

func (s *Server) handleOperatorSession(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{"account_id": p.AccountID, "name": p.Name})
}

type operatorTeamMatchJSON struct {
	Team        string   `json:"team"`
	DisplayName string   `json:"display_name"`
	Owners      []string `json:"owners"`
}

func (s *Server) handleOperatorTeams(w http.ResponseWriter, r *http.Request) {
	matches, err := s.store.SearchTeams(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		s.writeInternalError(w, r, "operator team search", err)
		return
	}
	out := make([]operatorTeamMatchJSON, 0, len(matches))
	for _, m := range matches {
		out = append(out, operatorTeamMatchJSON{
			Team: string(m.Slug), DisplayName: m.DisplayName, Owners: append([]string{}, m.Owners...),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": out})
}

type operatorEventJSON struct {
	At    int64          `json:"at"`
	Kind  string         `json:"kind"`
	Actor string         `json:"actor,omitempty"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

type operatorTeamJSON struct {
	Team         string              `json:"team"`
	DisplayName  string              `json:"display_name"`
	Owners       []string            `json:"owners"`
	BalanceMicro int64               `json:"balance_micro"`
	Billing      billingTrustJSON    `json:"billing"`
	Frozen       bool                `json:"frozen"`
	Holds        []string            `json:"holds"`
	Events       []operatorEventJSON `json:"events"`
}

const operatorEventLimit = 50

func (s *Server) handleOperatorTeam(w http.ResponseWriter, r *http.Request) {
	t, ok := s.namedTenant(w, r, r.PathValue("team"))
	if !ok {
		return
	}
	out, err := operatorTeamView(r.Context(), t)
	if err != nil {
		s.writeInternalError(w, r, "operator team read", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func operatorTeamView(ctx context.Context, t *store.Tenant) (operatorTeamJSON, error) {
	info, err := t.Info(ctx)
	members, merr := t.Members(ctx)
	credit, cerr := t.CreditState(ctx, creditsBurnWindow)
	standing, serr := t.BillingStanding(ctx, time.Now())
	freeze, ferr := t.CreditFreeze(ctx)
	events, eerr := t.BusinessEvents(ctx, "")
	if err := errors.Join(err, merr, cerr, serr, ferr, eerr); err != nil {
		return operatorTeamJSON{}, err
	}
	out := operatorTeamJSON{
		Team: string(t.Team()), DisplayName: info.DisplayName, Owners: []string{},
		BalanceMicro: credit.BalanceMicro, Billing: billingTrustBody(t.Team(), standing),
		Frozen: freeze.Frozen, Holds: append([]string{}, freeze.Disputes...), Events: []operatorEventJSON{},
	}
	for _, m := range members {
		if m.Role == store.RoleOwner {
			out.Owners = append(out.Owners, m.Email)
		}
	}
	for _, ev := range slices.Backward(events[max(len(events)-operatorEventLimit, 0):]) {
		out.Events = append(out.Events, operatorEventJSON{At: ev.At.Unix(), Kind: ev.Kind, Actor: ev.Actor, Attrs: ev.Attrs})
	}
	return out, nil
}

type operatorActionReq struct {
	Reason      string `json:"reason"`
	AmountCents int64  `json:"amount_cents,omitempty"`
	// safety: a retried grant carries the same key and finds the grant it already made.
	Key string `json:"key,omitempty"`
}

func decodeOperatorAction(w http.ResponseWriter, r *http.Request) (operatorActionReq, bool) {
	var req operatorActionReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return req, false
	}
	if req.Reason = strings.TrimSpace(req.Reason); req.Reason == "" || len(req.Reason) > 500 {
		writeError(w, http.StatusBadRequest, errors.New("reason is required, up to 500 characters"))
		return req, false
	}
	return req, true
}

func (s *Server) handleOperatorGrant(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeOperatorAction(w, r)
	if !ok {
		return
	}
	if req.AmountCents <= 0 || req.AmountCents > store.MaxPurchaseLimitCents {
		writeError(w, http.StatusBadRequest, fmt.Errorf("amount_cents must be 1 to %d", store.MaxPurchaseLimitCents))
		return
	}
	if req.Key = strings.TrimSpace(req.Key); req.Key == "" {
		writeError(w, http.StatusBadRequest, errors.New("key is required, so a retried grant grants once"))
		return
	}
	t, ok := s.namedTenant(w, r, r.PathValue("team"))
	if !ok {
		return
	}
	res, err := t.RecordCreditGrant(r.Context(), store.CreditGrantRequest{
		Kind: store.CreditGrantFree, AmountMicro: req.AmountCents * store.MicroCreditsPerCent,
		Reference: "console:" + req.Key, CreatedBy: operatorActor(r), Reason: req.Reason,
	})
	if s.writeBalanceCapRefusal(w, err) {
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, creditGrantToJSON(res.Grant))
}

func (s *Server) handleOperatorFreeze(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeOperatorAction(w, r)
	if !ok {
		return
	}
	t, ok := s.namedTenant(w, r, r.PathValue("team"))
	if !ok {
		return
	}
	if _, err := t.HoldByOperator(r.Context(), operatorActor(r), req.Reason, time.Now()); err != nil {
		s.writeInternalError(w, r, "operator freeze", err)
		return
	}
	s.writeOperatorFreeze(w, r, t)
}

func (s *Server) handleOperatorUnfreeze(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeOperatorAction(w, r)
	if !ok {
		return
	}
	t, ok := s.namedTenant(w, r, r.PathValue("team"))
	if !ok {
		return
	}
	if _, err := t.ReleaseOperatorHolds(r.Context(), operatorActor(r), req.Reason, time.Now()); err != nil {
		s.writeInternalError(w, r, "operator unfreeze", err)
		return
	}
	s.writeOperatorFreeze(w, r, t)
}

func (s *Server) writeOperatorFreeze(w http.ResponseWriter, r *http.Request, t *store.Tenant) {
	freeze, err := t.CreditFreeze(r.Context())
	if err != nil {
		s.writeInternalError(w, r, "operator read freeze", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"team": string(t.Team()), "frozen": freeze.Frozen, "holds": append([]string{}, freeze.Disputes...),
	})
}
