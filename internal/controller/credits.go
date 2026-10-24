package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
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

	StorageChargedMicro       int64 `json:"storage_charged_micro"`
	StorageRateMicroPerGBDay  int64 `json:"storage_rate_micro_per_gb_day"`
	StorageFreeAllowanceBytes int64 `json:"storage_free_allowance_bytes"`

	BurnWindowSeconds int64 `json:"burn_window_seconds"`
	BurnMicro         int64 `json:"burn_micro"`
	MicroPerCredit    int64 `json:"micro_per_credit"`
	CreditsPerDollar  int64 `json:"credits_per_dollar"`
}

type creditSettingsJSON struct {
	RateMicroPerSecond        int64            `json:"rate_micro_per_second"`
	RateTable                 []creditRateJSON `json:"rate_table"`
	RateTableSet              bool             `json:"rate_table_set"`
	WarmCPUClassCores         int64            `json:"warm_cpu_class_cores"`
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
	StorageRateMicroPerGBDay  *int64             `json:"storage_rate_micro_per_gb_day,omitempty"`
	StorageFreeAllowanceBytes *int64             `json:"storage_free_allowance_bytes,omitempty"`
}

func (r setCreditSettingsReq) update() store.CreditSettingsUpdate {
	out := store.CreditSettingsUpdate{
		RateMicroPerSecond:        r.RateMicroPerSecond,
		WarmCPUClassCores:         r.WarmCPUClassCores,
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

func (s *Server) handleCreditsShow(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.creditReader(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	state, err := tenant.CreditState(r.Context(), creditsBurnWindow)
	if err != nil {
		return engineLookupError(err)
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

		StorageChargedMicro:       state.StorageChargedMicro,
		StorageRateMicroPerGBDay:  state.StorageRateMicroPerGBDay,
		StorageFreeAllowanceBytes: state.StorageFreeAllowanceBytes,

		BurnWindowSeconds: int64(creditsBurnWindow.Seconds()),
		BurnMicro:         state.BurnMicro,
		MicroPerCredit:    store.MicroCreditsPerCredit,
		CreditsPerDollar:  store.CreditsPerDollar,
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleCreditsSettingsShow(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	if s.operator == nil {
		return routepolicy.MissingEndpointPolicy()
	}
	settings, err := s.operator.CreditSettings(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, creditSettingsToJSON(settings))
	return nil
}

func (s *Server) handleCreditsSettingsSet(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	var body setCreditSettingsReq
	if err := decodeJSON(r, &body); err != nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	writer, err := s.consoleWriter()
	if err != nil {
		return err
	}
	settings, err := writer.ConsoleSetCreditSettings(r.Context(), authorized, body.update())
	if err != nil {
		if errors.Is(err, store.ErrInvalidCreditSetting) {
			return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
		}
		return err
	}
	s.logger.Info("credit settings set",
		"rate_micro_per_second", settings.RateMicroPerSecond,
		"storage_rate_micro_per_gb_day", settings.StorageRateMicroPerGBDay,
		"storage_free_allowance_bytes", settings.StorageFreeAllowanceBytes)
	writeJSON(w, http.StatusOK, creditSettingsToJSON(settings))
	return nil
}

func creditSettingsToJSON(settings store.CreditSettings) creditSettingsJSON {
	return creditSettingsJSON{
		RateMicroPerSecond:        settings.RateMicroPerSecond,
		RateTable:                 creditRateTableToJSON(settings.RateTable),
		RateTableSet:              settings.RateTableSet,
		WarmCPUClassCores:         settings.WarmCPUClassCores,
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

func (s *Server) handleCreditsHistory(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	limit := 0
	if len(r.URL.Query()["limit"]) > 1 {
		return invalidEngineBody("limit must appear once")
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return invalidEngineBody("limit must be a non-negative integer")
		}
		if v > store.CreditHistoryMaxLimit {
			return invalidEngineBody(fmt.Sprintf("limit must not exceed %d rows of each kind", store.CreditHistoryMaxLimit))
		}
		limit = v
	}
	tenant, err := s.creditReader(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	grants, err := tenant.ListCreditGrants(r.Context(), limit)
	if err != nil {
		return engineLookupError(err)
	}
	charges, err := tenant.ListCreditCharges(r.Context(), limit)
	if err != nil {
		return engineLookupError(err)
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
	return nil
}

func creditGrantToJSON(g store.CreditGrant) creditGrantJSON {
	return creditGrantJSON{
		ID: g.ID, Kind: g.Kind, AmountMicro: g.AmountMicro,
		Reference: g.Reference, Reverses: g.Reverses,
		CreatedBy: g.CreatedBy, CreatedAt: g.CreatedAt.Unix(),
	}
}

// creditsRefusedCode identifies a claim refused because the balance cannot pay its first slice.
const creditsRefusedCode = "insufficient_credits"

// creditsFrozenCode identifies a claim refused by a billing hold.
const creditsFrozenCode = "credits_frozen"

// safety: a runner tells an unfunded or held team apart from a transport failure and keeps polling rather than
// retrying the request.
func creditsRefusal(refused *store.InsufficientCreditsError) *apierr.Error {
	reason := creditsRefusedCode
	if refused.Hold != "" {
		reason = creditsFrozenCode
	}
	return &apierr.Error{Code: apierr.InsufficientCredit, Message: refused.Error(), Details: map[string]any{"reason": reason, "hold": refused.Hold}}
}

// unpricedCPUClassCode identifies a claim whose CPU class has no rate.
const unpricedCPUClassCode = "unpriced_cpu_class"
