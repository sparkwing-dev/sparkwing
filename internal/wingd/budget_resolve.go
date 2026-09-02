package wingd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
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

func BudgetConfigPath() (string, error) {
	return fssecure.ConfigFile("budget")
}

func ResolveBudget(flagValue string) (ResolvedBudget, error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		return parseBudgetFrom(v, BudgetSourceFlag, "--budget")
	}
	if v := strings.TrimSpace(os.Getenv(BudgetEnv)); v != "" {
		return parseBudgetFrom(v, BudgetSourceEnv, BudgetEnv)
	}
	path, err := BudgetConfigPath()
	if err != nil {
		return ResolvedBudget{Source: BudgetSourceUnset}, nil
	}
	v, err := readBudgetFile(path)
	if err != nil {
		return ResolvedBudget{}, err
	}
	if v != "" {
		return parseBudgetFrom(v, BudgetSourceConfig, path)
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

func readBudgetFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read machine budget %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line, nil
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read machine budget %s: %w", path, err)
	}
	return "", nil
}
