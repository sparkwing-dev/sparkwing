package wingd

import (
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const BudgetEnv = "SPARKWING_BUDGET"

type BudgetSource = wingwire.BudgetSource

const (
	BudgetSourceUnset   = wingwire.BudgetSourceUnset
	BudgetSourceFlag    = wingwire.BudgetSourceFlag
	BudgetSourceEnv     = wingwire.BudgetSourceEnv
	BudgetSourceConfig  = wingwire.BudgetSourceConfig
	BudgetSourceUnknown = wingwire.BudgetSourceUnknown
)

type ResolvedBudget struct {
	Budget Budget

	Source BudgetSource

	Origin string
}

func (r ResolvedBudget) IsSet() bool { return r.Source != "" && r.Source != BudgetSourceUnset }

func ResolveBudget(flagValue string) (ResolvedBudget, error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		return parseBudgetFrom(v, BudgetSourceFlag, "--budget")
	}
	if v := strings.TrimSpace(os.Getenv(BudgetEnv)); v != "" {
		return parseBudgetFrom(v, BudgetSourceEnv, BudgetEnv)
	}
	raw, path, err := readAdmissionSection()
	if err != nil {
		return ResolvedBudget{}, fmt.Errorf("machine budget: %w", err)
	}
	if v := strings.TrimSpace(raw.Budget); v != "" {
		return parseBudgetFrom(v, BudgetSourceConfig, path+" admission.budget")
	}
	return ResolvedBudget{Source: BudgetSourceUnset}, nil
}

func parseBudgetFrom(raw string, src BudgetSource, origin string) (ResolvedBudget, error) {
	b, err := ParseBudget(raw)
	if err != nil {
		return ResolvedBudget{}, fmt.Errorf("machine budget from %s: %w", origin, err)
	}
	if !b.IsSet() {
		return ResolvedBudget{Source: BudgetSourceUnset}, nil
	}
	return ResolvedBudget{Budget: b, Source: src, Origin: origin}, nil
}

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
