package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: recent spend beside the balance is what says how long the balance lasts.
const creditsBurnWindow = 24 * time.Hour

type creditStateJSON struct {
	BalanceMicro       int64  `json:"balance_micro"`
	GrantedMicro       int64  `json:"granted_micro"`
	ChargedMicro       int64  `json:"charged_micro"`
	RateMicroPerSecond int64  `json:"rate_micro_per_second"`
	GraceSeconds       int64  `json:"grace_seconds"`
	BurnWindowSeconds  int64  `json:"burn_window_seconds"`
	BurnMicro          int64  `json:"burn_micro"`
	ExhaustedAt        *int64 `json:"exhausted_at,omitempty"`
	MicroPerCredit     int64  `json:"micro_per_credit"`
	CreditsPerDollar   int64  `json:"credits_per_dollar"`
}

type creditGrantJSON struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	AmountMicro int64  `json:"amount_micro"`
	Reference   string `json:"reference,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

type creditChargeJSON struct {
	ID          string `json:"id"`
	RunID       string `json:"run_id"`
	NodeID      string `json:"node_id"`
	TokenPrefix string `json:"token_prefix"`
	Seconds     int64  `json:"seconds"`
	AmountMicro int64  `json:"amount_micro"`
	ChargedAt   int64  `json:"charged_at"`
}

type creditHistoryJSON struct {
	Grants  []creditGrantJSON  `json:"grants"`
	Charges []creditChargeJSON `json:"charges"`
}

type createGrantReq struct {
	Kind        string `json:"kind"`
	AmountMicro int64  `json:"amount_micro"`
	Reference   string `json:"reference,omitempty"`
}

func (s *Server) handleCreditsShow(w http.ResponseWriter, r *http.Request) {
	state, err := s.store.CreditState(r.Context(), creditsBurnWindow)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := creditStateJSON{
		BalanceMicro:       state.BalanceMicro,
		GrantedMicro:       state.GrantedMicro,
		ChargedMicro:       state.ChargedMicro,
		RateMicroPerSecond: state.RateMicroPerSecond,
		GraceSeconds:       state.GraceSeconds,
		BurnWindowSeconds:  int64(creditsBurnWindow.Seconds()),
		BurnMicro:          state.BurnMicro,
		MicroPerCredit:     store.MicroCreditsPerCredit,
		CreditsPerDollar:   store.CreditsPerDollar,
	}
	if state.ExhaustedAt != nil {
		v := state.ExhaustedAt.Unix()
		out.ExhaustedAt = &v
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreditsGrant(w http.ResponseWriter, r *http.Request) {
	var req createGrantReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !store.ValidCreditGrantKind(req.Kind) {
		writeError(w, http.StatusBadRequest, errors.New("kind must be free or paid"))
		return
	}
	if req.AmountMicro <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("amount_micro must be positive"))
		return
	}
	who := authwire.AnonymousPrincipal
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		who = p.Name
	}
	grant, err := s.store.GrantCredits(r.Context(), req.Kind, req.AmountMicro, req.Reference, who)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Info("credits granted",
		"kind", grant.Kind, "amount_micro", grant.AmountMicro,
		"reference", grant.Reference, "by", who)
	writeJSON(w, http.StatusCreated, creditGrantToJSON(*grant))
}

func (s *Server) handleCreditsHistory(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a non-negative integer"))
			return
		}
		limit = v
	}
	grants, err := s.store.ListCreditGrants(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	charges, err := s.store.ListCreditCharges(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := creditHistoryJSON{
		Grants:  make([]creditGrantJSON, 0, len(grants)),
		Charges: make([]creditChargeJSON, 0, len(charges)),
	}
	for _, g := range grants {
		out.Grants = append(out.Grants, creditGrantToJSON(g))
	}
	for _, c := range charges {
		out.Charges = append(out.Charges, creditChargeJSON{
			ID: c.ID, RunID: c.RunID, NodeID: c.NodeID, TokenPrefix: c.TokenPrefix,
			Seconds: c.Seconds, AmountMicro: c.AmountMicro, ChargedAt: c.ChargedAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func creditGrantToJSON(g store.CreditGrant) creditGrantJSON {
	return creditGrantJSON{
		ID: g.ID, Kind: g.Kind, AmountMicro: g.AmountMicro,
		Reference: g.Reference, CreatedBy: g.CreatedBy, CreatedAt: g.CreatedAt.Unix(),
	}
}

// safety: a runner tells this apart from a transport failure and keeps polling
// rather than retrying the request.
type creditsRefusalJSON struct {
	Error         string `json:"error"`
	Code          string `json:"code"`
	BalanceMicro  int64  `json:"balance_micro"`
	RequiredMicro int64  `json:"required_micro"`
}

// CreditsRefusedCode is the machine-readable code on a 402 from a claim or
// heartbeat the ledger refused.
const CreditsRefusedCode = "insufficient_credits"

// safety: a false second result means the 402 refusal is already written, and a
// token the operator never marked metered is neither checked nor charged.
func (s *Server) meteredClaimAllowed(w http.ResponseWriter, r *http.Request) (metered, allowed bool) {
	prefix := claimIdentity(r).TokenPrefix
	if prefix == "" {
		return false, true
	}
	ctx := r.Context()
	metered, err := s.store.TokenMetered(ctx, prefix)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return false, false
	}
	if !metered {
		return false, true
	}
	balance, err := s.store.CreditBalanceMicro(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return true, false
	}
	required, err := s.store.CreditClaimFloorMicro(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return true, false
	}
	if balance >= required {
		return true, true
	}
	s.noteCreditsBlocked(r, balance, required)
	writeJSON(w, http.StatusPaymentRequired, creditsRefusalJSON{
		Error:         store.ErrInsufficientCredits.Error(),
		Code:          CreditsRefusedCode,
		BalanceMicro:  balance,
		RequiredMicro: required,
	})
	return true, false
}

// safety: the poller asks twice a second, so the waiting run records the
// refusal once per node rather than on every poll.
func (s *Server) noteCreditsBlocked(r *http.Request, balance, required int64) {
	ctx := r.Context()
	runID, nodeID, err := s.store.OldestWaitingReadyNode(ctx)
	if err != nil || runID == "" {
		return
	}
	payload, err := json.Marshal(map[string]int64{
		"balance_micro": balance, "required_micro": required,
	})
	if err != nil {
		return
	}
	wrote, err := s.store.AppendEventOnce(ctx, runID, nodeID, store.EventKindCreditsBlocked, payload)
	if err != nil {
		s.logger.Warn("recording a credit-blocked claim failed",
			"run_id", runID, "node_id", nodeID, "err", err)
		return
	}
	if wrote {
		s.logger.Warn("claim refused: the credit balance is spent",
			"run_id", runID, "node_id", nodeID,
			"balance_micro", balance, "required_micro", required)
	}
}

// safety: anchoring at the claim bills the first heartbeat from the claim
// rather than from itself.
func (s *Server) startMetering(r *http.Request, n *store.Node) {
	if n == nil {
		return
	}
	if err := s.store.StartNodeMetering(r.Context(), n.RunID, n.NodeID, time.Now()); err != nil {
		s.logger.Warn("anchoring a metered node's charge window failed",
			"run_id", n.RunID, "node_id", n.NodeID, "err", err)
	}
}

// safety: a token the operator never marked metered is never charged, so an
// install with no metered token behaves as it did before the ledger existed.
func (s *Server) chargeMeteredHeartbeat(r *http.Request, runID, nodeID string) (stop bool) {
	prefix := claimIdentity(r).TokenPrefix
	if prefix == "" {
		return false
	}
	ctx := r.Context()
	metered, err := s.store.TokenMetered(ctx, prefix)
	if err != nil || !metered {
		return false
	}
	res, err := s.store.ChargeNodeCredits(ctx, runID, nodeID, prefix, time.Now())
	if err != nil {
		s.logger.Warn("charging a metered node failed",
			"run_id", runID, "node_id", nodeID, "err", err)
		return false
	}
	if !res.Cancel {
		return false
	}
	payload, err := json.Marshal(map[string]any{
		"balance_micro":   res.BalanceMicro,
		"exhausted_for_s": int64(res.ExhaustedFor.Seconds()),
	})
	if err != nil {
		return true
	}
	if _, err := s.store.AppendEventOnce(ctx, runID, nodeID, store.EventKindCreditsExhausted, payload); err != nil {
		s.logger.Warn("recording an exhausted-credit cancellation failed",
			"run_id", runID, "node_id", nodeID, "err", err)
	}
	s.logger.Warn("cancelling a node: the credit balance stayed spent past the grace period",
		"run_id", runID, "node_id", nodeID,
		"balance_micro", res.BalanceMicro,
		"exhausted_for_s", int64(res.ExhaustedFor.Seconds()))
	return true
}
