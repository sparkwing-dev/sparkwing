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
	BalanceMicro       int64            `json:"balance_micro"`
	GrantedMicro       int64            `json:"granted_micro"`
	ReversedMicro      int64            `json:"reversed_micro"`
	ChargedMicro       int64            `json:"charged_micro"`
	RateMicroPerSecond int64            `json:"rate_micro_per_second"`
	RateTable          []creditRateJSON `json:"rate_table"`
	RateTableSet       bool             `json:"rate_table_set"`
	GraceSeconds       int64            `json:"grace_seconds"`
	MaxChargeSeconds   int64            `json:"max_charge_seconds"`
	BurnWindowSeconds  int64            `json:"burn_window_seconds"`
	BurnMicro          int64            `json:"burn_micro"`
	ExhaustedAt        *int64           `json:"exhausted_at,omitempty"`
	MicroPerCredit     int64            `json:"micro_per_credit"`
	CreditsPerDollar   int64            `json:"credits_per_dollar"`
}

type creditSettingsJSON struct {
	RateMicroPerSecond int64            `json:"rate_micro_per_second"`
	RateTable          []creditRateJSON `json:"rate_table"`
	RateTableSet       bool             `json:"rate_table_set"`
	GraceSeconds       int64            `json:"grace_seconds"`
	MaxChargeSeconds   int64            `json:"max_charge_seconds"`
	MicroPerCredit     int64            `json:"micro_per_credit"`
	CreditsPerDollar   int64            `json:"credits_per_dollar"`
}

// safety: the wire shape is one entry per cpu class, so a caller reads the
// ladder in the order the ledger prices it.
type creditRateJSON struct {
	Cores          int64 `json:"cores"`
	MicroPerSecond int64 `json:"micro_per_second"`
}

// safety: a nil field leaves that setting where it stands, so a caller names
// only what it means to change and a new field disturbs no existing caller.
type setCreditSettingsReq struct {
	RateMicroPerSecond *int64             `json:"rate_micro_per_second,omitempty"`
	RateTable          *creditRateTableIn `json:"rate_table,omitempty"`
	GraceSeconds       *int64             `json:"grace_seconds,omitempty"`
	MaxChargeSeconds   *int64             `json:"max_charge_seconds,omitempty"`
}

// safety: operators write the table both ways, so a body may name it as a list
// of entries or as an object keyed by cores.
type creditRateTableIn struct {
	table store.CreditRateTable
}

func (t *creditRateTableIn) UnmarshalJSON(raw []byte) error {
	var list []creditRateJSON
	if err := json.Unmarshal(raw, &list); err == nil {
		t.table = make(store.CreditRateTable, 0, len(list))
		for _, entry := range list {
			t.table = append(t.table,
				store.CreditRate{Cores: entry.Cores, MicroPerSecond: entry.MicroPerSecond})
		}
		return nil
	}
	var keyed map[string]int64
	if err := json.Unmarshal(raw, &keyed); err != nil {
		return errors.New(
			"rate_table must be a list of {cores, micro_per_second} or an object keyed by cores")
	}
	t.table = make(store.CreditRateTable, 0, len(keyed))
	for key, micro := range keyed {
		cores, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			return fmt.Errorf("rate_table key %q is not a number of cores", key)
		}
		t.table = append(t.table, store.CreditRate{Cores: cores, MicroPerSecond: micro})
	}
	return nil
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
	ID                 string `json:"id"`
	RunID              string `json:"run_id"`
	NodeID             string `json:"node_id"`
	TokenPrefix        string `json:"token_prefix"`
	Kind               string `json:"kind"`
	Seconds            int64  `json:"seconds"`
	AmountMicro        int64  `json:"amount_micro"`
	CPUClassCores      int64  `json:"cpu_class_cores,omitempty"`
	RateMicroPerSecond int64  `json:"rate_micro_per_second,omitempty"`
	ChargedAt          int64  `json:"charged_at"`
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
		RateTable:          creditRateTableToJSON(state.RateTable),
		RateTableSet:       state.RateTableSet,
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

func (s *Server) handleCreditsSettingsShow(w http.ResponseWriter, r *http.Request) {
	out, err := s.creditSettingsView(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreditsSettingsSet(w http.ResponseWriter, r *http.Request) {
	var req setCreditSettingsReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.refuseDerivedRateWrite(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.applyCreditSettings(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.creditSettingsView(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// safety: every field is checked before any of them is written, so a body that
// names one good setting and one bad one leaves the ledger as it was.
func (r setCreditSettingsReq) validate() error {
	if r.RateMicroPerSecond == nil && r.RateTable == nil &&
		r.GraceSeconds == nil && r.MaxChargeSeconds == nil {
		return errors.New(
			"name at least one of rate_micro_per_second, rate_table, grace_seconds, max_charge_seconds")
	}
	if r.RateMicroPerSecond != nil && *r.RateMicroPerSecond <= 0 {
		return errors.New("rate_micro_per_second must be positive")
	}
	if r.RateTable != nil {
		if err := r.RateTable.table.Validate(); err != nil {
			return err
		}
	}
	if r.GraceSeconds != nil && *r.GraceSeconds < 0 {
		return errors.New("grace_seconds must not be negative")
	}
	if r.MaxChargeSeconds != nil && *r.MaxChargeSeconds < store.MinCreditMaxChargeSeconds {
		return fmt.Errorf("max_charge_seconds must be at least %d, the heartbeat interval",
			store.MinCreditMaxChargeSeconds)
	}
	return nil
}

// safety: with a table stored the scalar is the four-core entry under another
// name, so writing it alone would move one class without saying so; the caller
// is told to write the table instead.
func (s *Server) refuseDerivedRateWrite(r *http.Request, req setCreditSettingsReq) error {
	if req.RateMicroPerSecond == nil || req.RateTable != nil {
		return nil
	}
	set, err := s.store.CreditRateTableSet(r.Context())
	if err != nil {
		return err
	}
	if !set {
		return nil
	}
	return errors.New(
		"rate_micro_per_second is the four-core entry of the rate table; write rate_table instead")
}

// safety: the table carries the four-core price into the single rate setting,
// so it is written first and a body naming both ends on the scalar the caller
// asked for.
func (s *Server) applyCreditSettings(r *http.Request, req setCreditSettingsReq) error {
	ctx := r.Context()
	if req.RateTable != nil {
		if err := s.store.SetCreditRateTable(ctx, req.RateTable.table); err != nil {
			return err
		}
		s.logger.Info("credit setting set",
			"setting", "rate_table", "classes", len(req.RateTable.table))
	}
	if req.RateMicroPerSecond != nil {
		if err := s.store.SetCreditRateMicroPerSecond(ctx, *req.RateMicroPerSecond); err != nil {
			return err
		}
		s.logger.Info("credit setting set",
			"setting", "rate_micro_per_second", "value", *req.RateMicroPerSecond)
	}
	if req.GraceSeconds != nil {
		if err := s.store.SetCreditGraceSeconds(ctx, *req.GraceSeconds); err != nil {
			return err
		}
		s.logger.Info("credit setting set", "setting", "grace_seconds", "value", *req.GraceSeconds)
	}
	if req.MaxChargeSeconds != nil {
		if err := s.store.SetCreditMaxChargeSeconds(ctx, *req.MaxChargeSeconds); err != nil {
			return err
		}
		s.logger.Info("credit setting set",
			"setting", "max_charge_seconds", "value", *req.MaxChargeSeconds)
	}
	return nil
}

func (s *Server) creditSettingsView(r *http.Request) (creditSettingsJSON, error) {
	ctx := r.Context()
	var out creditSettingsJSON
	rate, err := s.store.CreditRateMicroPerSecond(ctx)
	if err != nil {
		return out, err
	}
	grace, err := s.store.CreditGraceSeconds(ctx)
	if err != nil {
		return out, err
	}
	maxCharge, err := s.store.CreditMaxChargeSeconds(ctx)
	if err != nil {
		return out, err
	}
	table, err := s.store.CreditRateTable(ctx)
	if err != nil {
		return out, err
	}
	out.RateMicroPerSecond = rate
	out.RateTable = creditRateTableToJSON(table)
	tableSet, err := s.store.CreditRateTableSet(ctx)
	if err != nil {
		return out, err
	}
	out.RateTableSet = tableSet
	out.GraceSeconds = grace
	out.MaxChargeSeconds = maxCharge
	out.MicroPerCredit = store.MicroCreditsPerCredit
	out.CreditsPerDollar = store.CreditsPerDollar
	return out, nil
}

func creditRateTableToJSON(table store.CreditRateTable) []creditRateJSON {
	out := make([]creditRateJSON, 0, len(table))
	for _, entry := range table {
		out = append(out, creditRateJSON{Cores: entry.Cores, MicroPerSecond: entry.MicroPerSecond})
	}
	return out
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
			Kind: c.Kind, Seconds: c.Seconds, AmountMicro: c.AmountMicro,
			CPUClassCores: c.CPUClassCores, RateMicroPerSecond: c.RateMicroPerSecond,
			ChargedAt: c.ChargedAt.Unix(),
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

// UnpricedCPUClassCode is the machine-readable code on the refusal of a claim
// for a node asking for more cores than the credit rate table prices.
const UnpricedCPUClassCode = "unpriced_cpu_class"

// safety: a runner tells a priced-out node apart from a transport failure and
// stops asking for it until the operator adds the class.
type unpricedClassRefusalJSON struct {
	Error    string `json:"error"`
	Code     string `json:"code"`
	Cores    int64  `json:"cores"`
	MaxCores int64  `json:"max_cores"`
}

// safety: the ledger cannot price the node, so the claim is refused rather
// than billed at a class the operator never set.
func (s *Server) writeUnpricedClassRefusal(w http.ResponseWriter, r *http.Request, err error) bool {
	var unpriced *store.UnpricedCPUClassError
	if !errors.As(err, &unpriced) {
		return false
	}
	s.logger.Warn("claim refused: the credit rate table prices no class this large",
		"run_id", unpriced.RunID, "node_id", unpriced.NodeID,
		"cores", unpriced.Cores, "max_cores", unpriced.MaxCores)
	if unpriced.RunID != "" {
		// safety: every poller would otherwise retry this node forever, so the
		// run is failed with the reason rather than left waiting on a claim the
		// ledger cannot price.
		if failErr := s.store.FailNodeForUnpricedClass(r.Context(), unpriced, time.Now()); failErr != nil {
			s.logger.Warn("failing a node the rate table cannot price",
				"run_id", unpriced.RunID, "node_id", unpriced.NodeID, "err", failErr)
		}
	}
	writeJSON(w, http.StatusConflict, unpricedClassRefusalJSON{
		Error: unpriced.Error(), Code: UnpricedCPUClassCode,
		Cores: unpriced.Cores, MaxCores: unpriced.MaxCores,
	})
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
