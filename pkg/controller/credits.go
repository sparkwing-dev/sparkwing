package controller

import (
	"bytes"
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
	WarmCPUClassCores  int64            `json:"warm_cpu_class_cores"`
	GraceSeconds       int64            `json:"grace_seconds"`
	MaxChargeSeconds   int64            `json:"max_charge_seconds"`

	StorageChargedMicro       int64 `json:"storage_charged_micro"`
	StorageRateMicroPerGBDay  int64 `json:"storage_rate_micro_per_gb_day"`
	StorageFreeAllowanceBytes int64 `json:"storage_free_allowance_bytes"`

	BurnWindowSeconds int64  `json:"burn_window_seconds"`
	BurnMicro         int64  `json:"burn_micro"`
	ExhaustedAt       *int64 `json:"exhausted_at,omitempty"`
	MicroPerCredit    int64  `json:"micro_per_credit"`
	CreditsPerDollar  int64  `json:"credits_per_dollar"`
}

type creditSettingsJSON struct {
	RateMicroPerSecond        int64            `json:"rate_micro_per_second"`
	RateTable                 []creditRateJSON `json:"rate_table"`
	RateTableSet              bool             `json:"rate_table_set"`
	WarmCPUClassCores         int64            `json:"warm_cpu_class_cores"`
	GraceSeconds              int64            `json:"grace_seconds"`
	MaxChargeSeconds          int64            `json:"max_charge_seconds"`
	StorageRateMicroPerGBDay  int64            `json:"storage_rate_micro_per_gb_day"`
	StorageFreeAllowanceBytes int64            `json:"storage_free_allowance_bytes"`
	MicroPerCredit            int64            `json:"micro_per_credit"`
	CreditsPerDollar          int64            `json:"credits_per_dollar"`
}

// safety: the wire shape is one entry per cpu class, so a caller reads the
// ladder in the order the ledger prices it.
type creditRateJSON struct {
	Cores          int64 `json:"cores"`
	MicroPerSecond int64 `json:"micro_per_second"`
}

// safety: a nil field leaves that setting where it stands, which is what lets a
// caller send only the settings it means to change.
type setCreditSettingsReq struct {
	RateMicroPerSecond        *int64             `json:"rate_micro_per_second,omitempty"`
	RateTable                 *creditRateTableIn `json:"rate_table,omitempty"`
	WarmCPUClassCores         *int64             `json:"warm_cpu_class_cores,omitempty"`
	GraceSeconds              *int64             `json:"grace_seconds,omitempty"`
	MaxChargeSeconds          *int64             `json:"max_charge_seconds,omitempty"`
	StorageRateMicroPerGBDay  *int64             `json:"storage_rate_micro_per_gb_day,omitempty"`
	StorageFreeAllowanceBytes *int64             `json:"storage_free_allowance_bytes,omitempty"`
}

func (r setCreditSettingsReq) update() store.CreditSettingsUpdate {
	out := store.CreditSettingsUpdate{
		RateMicroPerSecond:        r.RateMicroPerSecond,
		WarmCPUClassCores:         r.WarmCPUClassCores,
		GraceSeconds:              r.GraceSeconds,
		MaxChargeSeconds:          r.MaxChargeSeconds,
		StorageRateMicroPerGBDay:  r.StorageRateMicroPerGBDay,
		StorageFreeAllowanceBytes: r.StorageFreeAllowanceBytes,
	}
	if r.RateTable != nil {
		out.RateTable = &r.RateTable.table
	}
	return out
}

func (r *setCreditSettingsReq) UnmarshalJSON(raw []byte) error {
	var wire struct {
		RateMicroPerSecond        optionalCreditInt64     `json:"rate_micro_per_second"`
		RateTable                 optionalCreditRateTable `json:"rate_table"`
		WarmCPUClassCores         optionalCreditInt64     `json:"warm_cpu_class_cores"`
		GraceSeconds              optionalCreditInt64     `json:"grace_seconds"`
		MaxChargeSeconds          optionalCreditInt64     `json:"max_charge_seconds"`
		StorageRateMicroPerGBDay  optionalCreditInt64     `json:"storage_rate_micro_per_gb_day"`
		StorageFreeAllowanceBytes optionalCreditInt64     `json:"storage_free_allowance_bytes"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return err
	}
	*r = setCreditSettingsReq{
		RateMicroPerSecond:        wire.RateMicroPerSecond.pointer(),
		RateTable:                 wire.RateTable.pointer(),
		WarmCPUClassCores:         wire.WarmCPUClassCores.pointer(),
		GraceSeconds:              wire.GraceSeconds.pointer(),
		MaxChargeSeconds:          wire.MaxChargeSeconds.pointer(),
		StorageRateMicroPerGBDay:  wire.StorageRateMicroPerGBDay.pointer(),
		StorageFreeAllowanceBytes: wire.StorageFreeAllowanceBytes.pointer(),
	}
	return nil
}

type optionalCreditInt64 struct {
	value int64
	set   bool
}

func (v *optionalCreditInt64) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("credit setting must be an integer, not null")
	}
	if err := json.Unmarshal(raw, &v.value); err != nil {
		return err
	}
	v.set = true
	return nil
}

func (v *optionalCreditInt64) pointer() *int64 {
	if !v.set {
		return nil
	}
	return &v.value
}

type optionalCreditRateTable struct {
	value creditRateTableIn
	set   bool
}

func (t *optionalCreditRateTable) UnmarshalJSON(raw []byte) error {
	if err := t.value.UnmarshalJSON(raw); err != nil {
		return err
	}
	t.set = true
	return nil
}

func (t *optionalCreditRateTable) pointer() *creditRateTableIn {
	if !t.set {
		return nil
	}
	return &t.value
}

// safety: operators write the table both ways, so a body may name it as a list
// of entries or as an object keyed by cores.
type creditRateTableIn struct {
	table store.CreditRateTable
}

func (t *creditRateTableIn) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("rate_table must be a list or object, not null")
	}
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
	Principal          string `json:"principal,omitempty"`
	Kind               string `json:"kind"`
	Seconds            int64  `json:"seconds"`
	AmountMicro        int64  `json:"amount_micro"`
	StorageBytes       int64  `json:"storage_bytes,omitempty"`
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
	// Team names the team whose balance the grant funds. The operator names
	// it because the grant route is the operator's, so the caller's own team
	// is never the one a payment was for. A reversal may leave it empty: the
	// payment it reverses belongs to exactly one team, and that is the team.
	Team string `json:"team,omitempty"`
	// Checkout names the payment session a paid grant settles, so the
	// checkout it opened stops counting against the team's balance cap.
	Checkout string `json:"checkout,omitempty"`
}

func (s *Server) handleCreditsShow(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	s.writeCreditState(w, r, tenant)
}

// handleTeamCreditsShow is the operator's read of one team's balance by slug.
func (s *Server) handleTeamCreditsShow(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.namedTenant(w, r, r.PathValue("team"))
	if !ok {
		return
	}
	s.writeCreditState(w, r, tenant)
}

// namedTenant resolves a team the operator names, answering 404 for one that
// is not registered.
func (s *Server) namedTenant(w http.ResponseWriter, r *http.Request, slug string) (*store.Tenant, bool) {
	t, err := s.tenantForTeam(r.Context(), store.Team(slug))
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		writeError(w, http.StatusNotFound, fmt.Errorf("team %q is not registered", slug))
		return nil, false
	}
	if err != nil {
		s.writeInternalError(w, r, "team handle", err)
		return nil, false
	}
	return t, true
}

func (s *Server) writeCreditState(w http.ResponseWriter, r *http.Request, tenant *store.Tenant) {
	state, err := tenant.CreditState(r.Context(), creditsBurnWindow)
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
		WarmCPUClassCores:  state.WarmCPUClassCores,
		GraceSeconds:       state.GraceSeconds,
		MaxChargeSeconds:   state.MaxChargeSeconds,

		StorageChargedMicro:       state.StorageChargedMicro,
		StorageRateMicroPerGBDay:  state.StorageRateMicroPerGBDay,
		StorageFreeAllowanceBytes: state.StorageFreeAllowanceBytes,

		BurnWindowSeconds: int64(creditsBurnWindow.Seconds()),
		BurnMicro:         state.BurnMicro,
		MicroPerCredit:    store.MicroCreditsPerCredit,
		CreditsPerDollar:  store.CreditsPerDollar,
	}
	if state.ExhaustedAt != nil {
		v := state.ExhaustedAt.Unix()
		out.ExhaustedAt = &v
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreditsSettingsShow(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.CreditSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, creditSettingsToJSON(settings))
}

func (s *Server) handleCreditsSettingsSet(w http.ResponseWriter, r *http.Request) {
	var body setCreditSettingsReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	settings, err := s.store.SetOperatorCreditSettings(r.Context(), body.update())
	if err != nil {
		if errors.Is(err, store.ErrInvalidCreditSetting) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("credit settings set",
		"rate_micro_per_second", settings.RateMicroPerSecond,
		"grace_seconds", settings.GraceSeconds,
		"max_charge_seconds", settings.MaxChargeSeconds,
		"storage_rate_micro_per_gb_day", settings.StorageRateMicroPerGBDay,
		"storage_free_allowance_bytes", settings.StorageFreeAllowanceBytes)
	writeJSON(w, http.StatusOK, creditSettingsToJSON(settings))
}

func creditSettingsToJSON(settings store.CreditSettings) creditSettingsJSON {
	return creditSettingsJSON{
		RateMicroPerSecond:        settings.RateMicroPerSecond,
		RateTable:                 creditRateTableToJSON(settings.RateTable),
		RateTableSet:              settings.RateTableSet,
		WarmCPUClassCores:         settings.WarmCPUClassCores,
		GraceSeconds:              settings.GraceSeconds,
		MaxChargeSeconds:          settings.MaxChargeSeconds,
		StorageRateMicroPerGBDay:  settings.StorageRateMicroPerGBDay,
		StorageFreeAllowanceBytes: settings.StorageFreeAllowanceBytes,
		MicroPerCredit:            store.MicroCreditsPerCredit,
		CreditsPerDollar:          store.CreditsPerDollar,
	}
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
	if req.Team == "" && req.Kind == store.CreditGrantReversal && req.Reverses != "" {
		team, found, err := s.store.PaidGrantTeam(r.Context(), req.Reverses)
		if err != nil {
			s.writeInternalError(w, r, "reversal team", err)
			return
		}
		if !found {
			writeError(w, http.StatusBadRequest,
				fmt.Errorf("no paid grant carries the reference %q", req.Reverses))
			return
		}
		req.Team = string(team)
	}
	// safety: on a controller serving several teams an unnamed grant would fund
	// whichever team the operator's token acts for, which is never the team a
	// payment was for, so the team is required there.
	if req.Team == "" && s.MultiTeam() {
		writeError(w, http.StatusBadRequest, errors.New("team is required: name the team whose balance the grant funds"))
		return
	}
	if req.Team == "" {
		req.Team = string(store.DefaultTeam)
	}
	tenant, ok := s.namedTenant(w, r, req.Team)
	if !ok {
		return
	}
	who := authwire.AnonymousPrincipal
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		who = p.Name
	}
	res, err := tenant.RecordCreditGrant(r.Context(), store.CreditGrantRequest{
		Kind: req.Kind, AmountMicro: req.AmountMicro,
		Reference: req.Reference, Reverses: req.Reverses, CreatedBy: who, Checkout: req.Checkout,
	})
	if errors.Is(err, store.ErrCreditGrantConflict) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if s.writeBalanceCapRefusal(w, err) {
		s.logger.Warn("credit grant refused at the team balance cap", "team", string(tenant.Team()),
			"kind", req.Kind, "amount_micro", req.AmountMicro, "reference", req.Reference, "err", err)
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
	s.logger.Info("credits granted", "team", string(tenant.Team()),
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	grants, err := tenant.ListCreditGrants(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	charges, err := tenant.ListCreditCharges(r.Context(), limit)
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
			Principal: c.Principal, Kind: c.Kind, Seconds: c.Seconds,
			AmountMicro: c.AmountMicro, StorageBytes: c.StorageBytes,
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
		s.noteCreditsBlocked(r, shortfall)
	}
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
// refusal once per node rather than on every poll. It lands on the node the
// claim was refused for, which is the claimant's own team's; the oldest
// waiting node on the controller may be another team's.
func (s *Server) noteCreditsBlocked(r *http.Request, shortfall *store.InsufficientCreditsError) {
	ctx := r.Context()
	runID, nodeID := shortfall.RunID, shortfall.NodeID
	if runID == "" {
		return
	}
	balance, required := shortfall.BalanceMicro, shortfall.RequiredMicro
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

// MeteredInProcessNodesCode is the machine-readable code on the 403 a metered
// trigger claim gets when it names no node runner that claims each node.
const MeteredInProcessNodesCode = "metered_inprocess_nodes"

// safety: credits are charged on node claims, and a trigger holder that runs
// nodes in its own process never makes one, so a metered credential claims a
// trigger only when it runs the nodes through k8s Jobs or warm capacity,
// which claim each node themselves.
func (s *Server) refuseMeteredInProcessNodes(w http.ResponseWriter, r *http.Request, nodeRunner string) bool {
	if nodeRunner == "k8s" || nodeRunner == "warm" || s.meteredTokenPrefix(r) == "" {
		return false
	}
	p, _ := PrincipalFromContext(r.Context())
	writeAuthError(w, http.StatusForbidden, authErrorBody{
		Code: MeteredInProcessNodesCode, Principal: p.label(),
		Message: store.ErrMeteredInProcessNodes.Error() + "; run the trigger runner as k8s or warm",
	})
	return true
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
