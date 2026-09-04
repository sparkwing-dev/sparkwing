package fleet

import "github.com/sparkwing-dev/sparkwing/pkg/store"

// PolicyProjection is the credential-free, static fleet policy suitable for
// local UI and audit events. It reports configured trust, never runtime
// eligibility, liveness, offers, or awards.
type PolicyProjection struct {
	Coordinator LocalPolicyProjection      `json:"coordinator"`
	Helpers     []ExecutorPolicyProjection `json:"helpers,omitempty"`
}

type LocalPolicyProjection struct {
	Name                string   `json:"name"`
	Placement           string   `json:"placement"`
	TrustedCapabilities []string `json:"trusted_capabilities,omitempty"`
	MaxConcurrent       int      `json:"max_concurrent"`
	Contribution        string   `json:"contribution"`
	LocalReserve        string   `json:"local_reserve,omitempty"`
}

type ExecutorPolicyProjection struct {
	Name                string                 `json:"name"`
	Kind                string                 `json:"kind"`
	Placement           string                 `json:"placement"`
	TrustedCapabilities []string               `json:"trusted_capabilities,omitempty"`
	BasePriority        int                    `json:"base_priority"`
	PriorityCeiling     int                    `json:"priority_ceiling"`
	MaxConcurrent       int                    `json:"max_concurrent"`
	Budget              store.ExecutorResource `json:"budget"`
}

func (c Config) PolicyProjection() PolicyProjection {
	projection := PolicyProjection{Coordinator: LocalPolicyProjection{
		Name: c.Local.Name, Placement: "coordinator",
		TrustedCapabilities: append([]string(nil), c.Local.Capabilities...),
		MaxConcurrent:       c.Local.MaxConcurrent, Contribution: c.Local.Contribution,
		LocalReserve: c.Local.LocalReserve,
	}}
	projection.Helpers = make([]ExecutorPolicyProjection, 0, len(c.Executors))
	for _, executor := range c.Executors {
		projection.Helpers = append(projection.Helpers, ExecutorPolicyProjection{
			Name: executor.Name, Kind: "agent", Placement: executor.Location,
			TrustedCapabilities: append([]string(nil), executor.Capabilities...),
			BasePriority:        executor.BasePriority, PriorityCeiling: executor.PriorityCeiling,
			MaxConcurrent: executor.MaxConcurrent, Budget: executor.Budget,
		})
	}
	return projection
}
