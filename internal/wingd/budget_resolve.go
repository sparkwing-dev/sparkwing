package wingd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// BudgetEnv is the environment override for the machine budget.
const BudgetEnv = "SPARKWING_BUDGET"

// BudgetSource names where the active machine budget was read from. It
// travels with the budget all the way to the queue view, because an
// override an operator cannot see is one they can neither trust nor
// revoke: the daemon is spawned on demand by whichever gate runs first,
// so the setting in force is often not the one the person reading the
// queue put there.
//
// The vocabulary lives in [wingwire] because it goes out on the wire;
// these aliases keep the daemon's own code reading in one package.
type BudgetSource = wingwire.BudgetSource

const (
	BudgetSourceUnset   = wingwire.BudgetSourceUnset
	BudgetSourceFlag    = wingwire.BudgetSourceFlag
	BudgetSourceEnv     = wingwire.BudgetSourceEnv
	BudgetSourceConfig  = wingwire.BudgetSourceConfig
	BudgetSourceUnknown = wingwire.BudgetSourceUnknown
)

// ResolvedBudget is a machine budget together with where it was read
// from, so every surface that reports the budget can also report which
// setting to edit to change it.
type ResolvedBudget struct {
	// Budget is the parsed budget. Zero when Source is
	// [BudgetSourceUnset].
	Budget Budget
	// Source names the kind of setting the budget came from.
	Source BudgetSource
	// Origin is the exact setting: the flag name, the environment
	// variable, or the config file path. Empty when nothing is set.
	Origin string
}

// IsSet reports whether a budget is actually in force.
func (r ResolvedBudget) IsSet() bool { return r.Source != "" && r.Source != BudgetSourceUnset }

// BudgetConfigPath is the on-disk machine-budget file, honoring
// XDG_CONFIG_HOME > $HOME/.config, matching where profiles.yaml and the
// version hold already live.
func BudgetConfigPath() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "sparkwing", "budget"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve budget config path: %w", err)
	}
	return filepath.Join(home, ".config", "sparkwing", "budget"), nil
}

// ResolveBudget resolves the machine budget in the wing family's
// documented order: an explicit --budget flag beats SPARKWING_BUDGET in
// the environment, which beats the config file.
//
// The config file is the only source that outlives the process that
// spawned the daemon. wingd is started on demand by whichever gate runs
// first, inheriting that process's environment, so an env-only setting
// belongs to the accident of who triggered the spawn rather than to the
// operator who meant to apply it.
//
// A value that will not parse fails rather than being dropped, whichever
// source it came from. A setting that silently does nothing is the exact
// failure this resolver exists to end, and an unparseable SPARKWING_BUDGET
// has always stopped daemon startup, so the config file carries no new
// risk. A missing config file is not an error: it is the ordinary case.
// Neither is a machine with no home directory to hold one, which has no
// durable budget rather than a broken one.
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

// parseBudgetFrom parses one source's raw value and labels the result
// with where it came from. A value that parses to nothing (an empty or
// comment-only setting) resolves as unset rather than as a source with no
// budget behind it.
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

// resolvedBudget reports the daemon's active budget with its source. A
// config assembled without going through [ResolveBudget] -- an embedding
// binary, a test -- carries a budget with no recorded source, reported as
// [BudgetSourceUnknown] so no surface names a setting that would not
// change it.
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

// readBudgetFile reads the first setting line from the machine-budget
// config file. Blank lines and # comments are skipped so an operator can
// record why a budget is in force next to the budget itself, which is the
// note the next person needs when they find admission capped and no one
// remembers who capped it. An absent file yields an empty value and no
// error.
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
