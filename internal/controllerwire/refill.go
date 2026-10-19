package controllerwire

import "context"

// RefillCaps are the standing tier's refill caps; a zero field is no cap.
type RefillCaps struct {
	AmountCents int64 `json:"amount_cents"`
	PerDay      int64 `json:"per_day"`
	WeekCents   int64 `json:"week_cents"`
}

// TeamRefill is the team's automatic refill as the billing response reports it; week and month are the last 7 and
// 30 days, and a zero team limit is none.
type TeamRefill struct {
	Enabled         bool       `json:"enabled"`
	ThresholdCents  int64      `json:"threshold_cents"`
	AmountCents     int64      `json:"amount_cents"`
	WeekLimitCents  int64      `json:"week_limit_cents"`
	MonthLimitCents int64      `json:"month_limit_cents"`
	MinAmountCents  int64      `json:"min_amount_cents"`
	MaxAmountCents  int64      `json:"max_amount_cents"`
	Caps            RefillCaps `json:"caps"`
	FailedAt        int64      `json:"failed_at,omitempty"`
	Failure         string     `json:"failure,omitempty"`
	Open            bool       `json:"open"`
	DayCount        int64      `json:"day_count"`
	WeekCents       int64      `json:"week_cents"`
	MonthCents      int64      `json:"month_cents"`
	Limit           string     `json:"limit,omitempty"`
}

// RefillWrite replaces the team's refill settings.
type RefillWrite struct {
	Enabled         bool  `json:"enabled"`
	ThresholdCents  int64 `json:"threshold_cents"`
	AmountCents     int64 `json:"amount_cents"`
	WeekLimitCents  int64 `json:"week_limit_cents"`
	MonthLimitCents int64 `json:"month_limit_cents"`
}

// BillingManager reads and changes the authenticated team's standing and automatic refill.
type BillingManager interface {
	ReadStanding(context.Context) (TeamStanding, error)
	RequestStanding(context.Context, StandingRequestWrite) (StandingRequest, error)
	ReadRefill(context.Context) (TeamRefill, error)
	SetRefill(context.Context, RefillWrite) error
}
