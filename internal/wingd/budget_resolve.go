package wingd

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type ResolvedBudget struct {
	Budget userconfig.Budget

	Source wingwire.BudgetSource

	Origin string
}

func ResolveBudget(flagValue string) (ResolvedBudget, error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		return parseBudgetFrom(v, wingwire.BudgetSourceFlag, "--budget")
	}
	var raw userconfig.AdmissionConfig
	path, _, err := userconfig.ReadDefault(userconfig.Admission, &raw)
	if err != nil {
		return ResolvedBudget{}, fmt.Errorf("machine budget: %w", err)
	}
	if raw.Budget != nil {
		return parseBudgetFrom(*raw.Budget, wingwire.BudgetSourceConfig, path+" admission.budget")
	}
	return ResolvedBudget{Source: wingwire.BudgetSourceUnset}, nil
}

func parseBudgetFrom(raw string, src wingwire.BudgetSource, origin string) (ResolvedBudget, error) {
	b, err := userconfig.ParseBudget(raw)
	if err != nil {
		return ResolvedBudget{}, fmt.Errorf("machine budget from %s: %w", origin, err)
	}
	if !b.IsSet() {
		return ResolvedBudget{Source: wingwire.BudgetSourceUnset}, nil
	}
	return ResolvedBudget{Budget: b, Source: src, Origin: origin}, nil
}

func (c Config) resolvedBudget() ResolvedBudget {
	if !c.Budget.IsSet() {
		return ResolvedBudget{Source: wingwire.BudgetSourceUnset}
	}
	src, origin := c.BudgetSource, c.BudgetOrigin
	if src == "" || src == wingwire.BudgetSourceUnset {
		src, origin = wingwire.BudgetSourceUnknown, ""
	}
	return ResolvedBudget{Budget: c.Budget, Source: src, Origin: origin}
}
