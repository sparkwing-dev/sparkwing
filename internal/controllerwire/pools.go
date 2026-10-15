package controllerwire

import "context"

// PoolDetail exposes server-granted reservations and access rules. Labels and
// caller-supplied repository names cannot create these grants.
type PoolDetail struct {
	Name    string     `json:"name"`
	Runners []string   `json:"runners,omitempty"`
	Rules   []PoolRule `json:"rules,omitempty"`
}

type PoolRule struct {
	ID       string   `json:"id"`
	Repos    []string `json:"repos"`
	Branches []string `json:"branches,omitempty"`
	Source   string   `json:"source,omitempty"`
	Route    bool     `json:"route"`
}

type Pools struct {
	Pools []PoolDetail `json:"pools"`
}

// PoolRuleWrite proposes selectors; the controller independently verifies repo
// authority and derives the team. IdempotencyKey travels only in the header.
type PoolRuleWrite struct {
	Repos          []string `json:"repos"`
	Branches       []string `json:"branches,omitempty"`
	Source         string   `json:"source,omitempty"`
	Route          bool     `json:"route"`
	IdempotencyKey string   `json:"-"`
}

type PoolRuleResult struct {
	Pool     string   `json:"pool"`
	Rule     PoolRule `json:"rule"`
	Replayed bool     `json:"replayed"`
}

type PoolRuleDeletion struct {
	Pool    string `json:"pool"`
	RuleID  string `json:"rule_id"`
	Deleted bool   `json:"deleted"`
}

// PoolManager uses the team-admin rule routes; it never infers grants locally.
type PoolManager interface {
	ListPools(context.Context) (Pools, error)
	AllowPool(context.Context, string, PoolRuleWrite) (PoolRuleResult, error)
	DisallowPool(context.Context, string, string) (PoolRuleDeletion, error)
}
