package wingd

import (
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type BudgetSource = wingwire.BudgetSource

const (
	BudgetSourceUnset   = wingwire.BudgetSourceUnset
	BudgetSourceFlag    = wingwire.BudgetSourceFlag
	BudgetSourceConfig  = wingwire.BudgetSourceConfig
	BudgetSourceUnknown = wingwire.BudgetSourceUnknown
)

type ResolvedBudget struct {
	Budget userconfig.Budget

	Source BudgetSource

	Origin string
}

func (r ResolvedBudget) IsSet() bool { return r.Source != "" && r.Source != BudgetSourceUnset }

func (c Config) resolvedBudget() ResolvedBudget {
	if !c.Budget.IsSet() {
		return ResolvedBudget{Source: BudgetSourceUnset}
	}
	src, origin := c.BudgetSource, c.BudgetOrigin
	if src == "" || src == BudgetSourceUnset {
		src, origin = BudgetSourceUnknown, ""
	}
	return ResolvedBudget{Budget: c.Budget, Source: src, Origin: origin}
}
