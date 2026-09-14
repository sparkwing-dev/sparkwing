package controller

import (
	"encoding/json"
	"errors"
	"fmt"
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
	ReversedMicro      int64  `json:"reversed_micro"`
	ChargedMicro       int64  `json:"charged_micro"`
	RateMicroPerSecond int64  `json:"rate_micro_per_second"`
	GraceSeconds       int64  `json:"grace_seconds"`
	MaxChargeSeconds   int64  `json:"max_charge_seconds"`
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
	Reverses    string `json:"reverses,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

type creditChargeJSON struct {
	ID          string `json:"id"`
	RunID       string `json:"run_id"`
	NodeID      string `json:"node_id"`
	TokenPrefix string `json:"token_prefix"`
	Kind        string `json:"kind"`
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
	Reverses    string `json:"reverses,omitempty"`
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
		ReversedMicro:      state.ReversedMicro,
		ChargedMicro:       state.ChargedMicro,
		RateMicroPerSecond: state.RateMicroPerSecond,
		GraceSeconds:       state.GraceSeconds,
		MaxChargeSeconds:   state.MaxChargeSeconds,
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
		writeError(w, http.StatusBadRequest, errors.New("kind must be free, paid or reversal"))
		return
	}
	if err := grantAmountRule(req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	who := authwire.AnonymousPrincipal
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		who = p.Name
	}
	res, err := s.store.RecordCreditGrant(r.Context(), store.CreditGrantRequest{
		Kind: req.Kind, AmountMicro: req.AmountMicro,
		Reference: req.Reference, Reverses: req.Reverses, CreatedBy: who,
	})
	if errors.Is(err, store.ErrCreditGrantConflict) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !res.Created {
		// safety: a payment webhook redelivers until it sees a 2xx, so the
		// reference it already granted answers with the row it wrote.
		s.logger.Info("credits already granted for this reference",
			"kind", res.Grant.Kind, "reference", res.Grant.Reference, "grant_id", res.Grant.ID)
		writeJSON(w, http.StatusOK, creditGrantToJSON(res.Grant))
		return
	}
	s.logger.Info("credits granted",
		"kind", res.Grant.Kind, "amount_micro", res.Grant.AmountMicro,
		"reference", res.Grant.Reference, "reverses", res.Grant.Reverses, "by", who)
	writeJSON(w, http.StatusCreated, creditGrantToJSON(res.Grant))
}

func grantAmountRule(req createGrantReq) error {
	if req.Kind == store.CreditGrantReversal {
		if req.AmountMicro >= 0 {
			return errors.New("amount_micro must be negative for a reversal")
		}
		return nil
	}
	if req.AmountMicro <= 0 {
		return errors.New("amount_micro must be positive")
	}
	return nil
}

func (s *Server) handleCreditsHistory(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a non-negative integer"))
			return
		}
		if v > store.CreditHistoryMaxLimit {
			writeError(w, http.StatusBadRequest, fmt.Errorf(
				"limit must not exceed %d rows of each kind", store.CreditHistoryMaxLimit))
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
			Kind: c.Kind, Seconds: c.Seconds, AmountMicro: c.AmountMicro, ChargedAt: c.ChargedAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func creditGrantToJSON(g store.CreditGrant) creditGrantJSON {
	return creditGrantJSON{
		ID: g.ID, Kind: g.Kind, AmountMicro: g.AmountMicro,
		Reference: g.Reference, Reverses: g.Reverses,
		CreatedBy: g.CreatedBy, CreatedAt: g.CreatedAt.Unix(),
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

// safety: the refusal is a standing condition, so it is recorded once against
// the run whose node is waiting rather than on every poll.
func (s *Server) writeCreditsRefusal(w http.ResponseWriter, r *http.Request, err error) bool {
	if !errors.Is(err, store.ErrInsufficientCredits) {
		return false
	}
	refusal := creditsRefusalJSON{
		Error: store.ErrInsufficientCredits.Error(),
		Code:  CreditsRefusedCode,
	}
	var shortfall *store.InsufficientCreditsError
	if errors.As(err, &shortfall) {
		refusal.BalanceMicro = shortfall.BalanceMicro
		refusal.RequiredMicro = shortfall.RequiredMicro
	}
	s.noteCreditsBlocked(r, refusal.BalanceMicro, refusal.RequiredMicro)
	writeJSON(w, http.StatusPaymentRequired, refusal)
	return true
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

// safety: a token the operator never marked metered is never charged, so an
// install with no metered token behaves as it did before the ledger existed.
func (s *Server) meteredTokenPrefix(r *http.Request) string {
	prefix := claimIdentity(r).TokenPrefix
	if prefix == "" {
		return ""
	}
	metered, err := s.store.TokenMetered(r.Context(), prefix)
	if err != nil || !metered {
		return ""
	}
	return prefix
}

// safety: the node's own claim carries the credential the ledger priced the
// work against, so a finish posted by another principal settles the same way,
// and a node whose credential was revoked mid-run still releases its charge
// window instead of holding a reservation open forever.
func (s *Server) settleFinishedNode(r *http.Request, runID, nodeID string) {
	settlement, err := s.nodeSettlement(r, runID, nodeID)
	if err != nil {
		return
	}
	s.settleNodeLedger(r, runID, nodeID, settlement)
	// safety: an open window means the ledger priced this node as cloud work,
	// whatever the credential says now, so a token un-metered mid-run is not
	// counted under both placements.
	if settlement.Metering == store.MeteringFree && !settlement.ChargeWindowOpen {
		addLocalNodeSeconds(settlement.Seconds)
	}
}

func (s *Server) nodeSettlement(r *http.Request, runID, nodeID string) (store.NodeSettlement, error) {
	settlement, err := s.store.NodeSettlement(r.Context(), runID, nodeID)
	if err != nil {
		s.logger.Warn("reading a node's settlement failed",
			"run_id", runID, "node_id", nodeID, "err", err)
	}
	return settlement, err
}

// safety: a balance spent past the grace period cancels the node in this same
// request, so the run records why instead of waiting out the lease.
func (s *Server) chargeMeteredHeartbeat(r *http.Request, runID, nodeID string) (stop bool) {
	prefix := s.meteredTokenPrefix(r)
	if prefix == "" {
		return false
	}
	ctx := r.Context()
	res, err := s.store.ChargeNodeCredits(ctx, runID, nodeID, prefix, time.Now())
	if err != nil {
		s.logger.Warn("charging a metered node failed",
			"run_id", runID, "node_id", nodeID, "err", err)
		return false
	}
	if res.ForgivenSeconds > 0 {
		s.logger.Warn("charge cap engaged; the gap since the previous charge is not billed",
			"run_id", runID, "node_id", nodeID, "forgiven_s", res.ForgivenSeconds)
	}
	if !res.Cancel {
		return false
	}
	s.cancelForExhaustedCredits(r, runID, nodeID, prefix, res)
	return true
}

func (s *Server) cancelForExhaustedCredits(
	r *http.Request, runID, nodeID, prefix string, res store.CreditChargeResult,
) {
	ctx := r.Context()
	payload, err := json.Marshal(map[string]any{
		"balance_micro":   res.BalanceMicro,
		"exhausted_for_s": int64(res.ExhaustedFor.Seconds()),
	})
	if err != nil {
		payload = nil
	}
	if _, err := s.store.AppendEventOnce(ctx, runID, nodeID, store.EventKindCreditsExhausted, payload); err != nil {
		s.logger.Warn("recording an exhausted-credit cancellation failed",
			"run_id", runID, "node_id", nodeID, "err", err)
	}
	if err := s.store.CancelNodeForExhaustedCredits(ctx, runID, nodeID, prefix, time.Now()); err != nil {
		s.logger.Error("cancelling a node for exhausted credits failed",
			"run_id", runID, "node_id", nodeID, "err", err)
		return
	}
	s.logger.Warn("cancelled a node: the credit balance stayed spent past the grace period",
		"run_id", runID, "node_id", nodeID,
		"balance_micro", res.BalanceMicro,
		"exhausted_for_s", int64(res.ExhaustedFor.Seconds()))
}

// safety: without this the tail between the last heartbeat and the finish is
// free, and the unused part of the claim reservation is never refunded.
func (s *Server) finalizeMeteredNode(r *http.Request, runID, nodeID string) {
	settlement, err := s.nodeSettlement(r, runID, nodeID)
	if err != nil {
		return
	}
	s.settleNodeLedger(r, runID, nodeID, settlement)
}

// safety: an open window is the only thing worth settling, and it is also what
// keeps a node in the reservation index, so it is released whatever the
// finishing principal presents.
func (s *Server) settleNodeLedger(r *http.Request, runID, nodeID string, settlement store.NodeSettlement) {
	if !settlement.ChargeWindowOpen {
		return
	}
	res, err := s.store.FinalizeNodeCredits(r.Context(), runID, nodeID, settlement.ClaimTokenPrefix, time.Now())
	if err != nil {
		s.logger.Warn("settling a metered node failed",
			"run_id", runID, "node_id", nodeID, "err", err)
		return
	}
	if res.ForgivenSeconds > 0 {
		s.logger.Warn("charge cap engaged at finish; the gap since the previous charge is not billed",
			"run_id", runID, "node_id", nodeID, "forgiven_s", res.ForgivenSeconds)
	}
}
