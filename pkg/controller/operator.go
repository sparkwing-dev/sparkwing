package controller

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// WithOperatorAccounts names accounts whose direct sign-in sessions may use the console.
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

func (s *Server) operatorAccount(id string) bool {
	set := s.operators.Load()
	return id != "" && set != nil && (*set)[id]
}

type operatorTeamMatchJSON struct {
	Team        string   `json:"team"`
	DisplayName string   `json:"display_name"`
	Owners      []string `json:"owners"`
}

func (s *Server) handleOperatorTeams(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	matches, err := s.store.SearchTeams(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		return err
	}
	out := make([]operatorTeamMatchJSON, 0, len(matches))
	for _, m := range matches {
		out = append(out, operatorTeamMatchJSON{
			Team: string(m.Slug), DisplayName: m.DisplayName, Owners: append([]string{}, m.Owners...),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": out})
	return nil
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

func (s *Server) handleOperatorTeam(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	t, err := s.consoleTenant(r.Context(), authorized.Target.Team)
	if err != nil {
		return err
	}
	out, err := operatorTeamView(r.Context(), t)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
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
	// safety: an unfreeze naming a hold releases that one, a payment dispute's
	// included; one naming none releases only the console's own holds.
	Hold string `json:"hold,omitempty"`
}

func decodeOperatorAction(r *http.Request) (operatorActionReq, error) {
	var req operatorActionReq
	if err := decodeJSON(r, &req); err != nil {
		return req, &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	reason, err := OperatorReason(req.Reason)
	if err != nil {
		return req, &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	req.Reason = reason
	return req, nil
}

// OperatorReason trims the reason an operator gives for acting on a team and
// refuses an empty one or one over 500 characters. The console and the
// sparkwing-controller admin command record it on the action's business event.
func OperatorReason(reason string) (string, error) {
	if reason = strings.TrimSpace(reason); reason == "" || len(reason) > 500 {
		return "", errors.New("reason is required, up to 500 characters")
	}
	return reason, nil
}

func (s *Server) handleOperatorGrant(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	req, err := decodeOperatorAction(r)
	if err != nil {
		return err
	}
	if req.AmountCents <= 0 || req.AmountCents > (store.MaxCreditGrantMicro/store.MicroCreditsPerCent) {
		return &apierr.Error{Code: apierr.BadRequest, Message: "amount_cents must be positive and fit the credit ledger"}
	}
	if req.Key = strings.TrimSpace(req.Key); req.Key == "" {
		return &apierr.Error{Code: apierr.BadRequest, Message: "key is required, so a retried grant grants once"}
	}
	writer, err := s.consoleWriter()
	if err != nil {
		return err
	}
	res, err := writer.ConsoleGrantCredit(r.Context(), authorized, store.CreditGrantRequest{
		Kind: store.CreditGrantFree, AmountMicro: req.AmountCents * store.MicroCreditsPerCent,
		Reference: "console:" + req.Key, CreatedBy: authorized.Principal.Capability.Subject.ID, Reason: req.Reason,
	})
	if errors.Is(err, store.ErrInvalidInput) {
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, creditGrantToJSON(res.Grant))
	return nil
}

func (s *Server) handleOperatorFreeze(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	req, err := decodeOperatorAction(r)
	if err != nil {
		return err
	}
	writer, err := s.consoleWriter()
	if err != nil {
		return err
	}
	freeze, err := writer.ConsoleFreeze(r.Context(), authorized, req.Reason)
	if err != nil {
		return engineLookupError(err)
	}
	writeOperatorFreeze(w, authorized.Target.Team, freeze)
	return nil
}

func (s *Server) handleOperatorUnfreeze(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	req, err := decodeOperatorAction(r)
	if err != nil {
		return err
	}
	writer, err := s.consoleWriter()
	if err != nil {
		return err
	}
	freeze, err := writer.ConsoleUnfreeze(r.Context(), authorized, strings.TrimSpace(req.Hold), req.Reason)
	if err != nil {
		return engineLookupError(err)
	}
	writeOperatorFreeze(w, authorized.Target.Team, freeze)
	return nil
}

func writeOperatorFreeze(w http.ResponseWriter, team string, freeze store.TeamCreditFreeze) {
	writeJSON(w, http.StatusOK, map[string]any{"team": team, "frozen": freeze.Frozen, "holds": append([]string{}, freeze.Disputes...)})
}

const operatorWaitlistPage = 50

func (s *Server) handleOperatorWaitlist(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	q := r.URL.Query()
	limit, offset := operatorWaitlistPage, 0
	for _, v := range []struct {
		name     string
		dst      *int
		min, max int
	}{{"limit", &limit, 1, maxWaitlistPage}, {"offset", &offset, 0, math.MaxInt32}} {
		if raw := q.Get(v.name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < v.min || n > v.max {
				return &apierr.Error{Code: apierr.BadRequest, Message: fmt.Sprintf("%s is %d to %d", v.name, v.min, v.max)}
			}
			*v.dst = n
		}
	}
	ctx := r.Context()
	waiting, err := s.store.NewestWaitlisted(ctx, limit, offset)
	counts, cerr := s.store.SignUpCounts(ctx, time.Now())
	if err := errors.Join(err, cerr); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": waitlistBody(waiting), "total": counts.Waitlisted})
	return nil
}
