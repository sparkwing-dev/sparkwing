package profile

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

// ChainSource tags which resolution rule produced a profile candidate.
type ChainSource string

const (
	ChainSourceFlag    ChainSource = "flag"    // --profile X
	ChainSourceProject ChainSource = "project" // sparkwing.yaml `profile:` field
	ChainSourceDefault ChainSource = "default" // profiles.yaml `default:` key
	ChainSourceBuiltin ChainSource = "builtin" // synthesized "laptop" fallback
)

// ConsideredEntry is one resolution rule that did not produce the
// selected profile, with a short human-readable reason. Renders from
// the `sparkwing profile` introspection command and the run_start
// envelope so an operator can see why a command went where it did.
type ConsideredEntry struct {
	Source ChainSource
	Name   string // candidate the rule would have offered; "" when the rule had none
	Reason string
}

// Chain records the resolution that picked the active profile.
// Selected names the winning profile; Source tags the rule that
// chose it. Considered lists every other rule, in precedence order,
// with why it was not selected.
type Chain struct {
	Selected   string
	Source     ChainSource
	Considered []ConsideredEntry
}

// BuiltinLaptopProfile returns the synthesized day-zero fallback: local
// SQLite state plus filesystem cache and logs under ~/.cache/sparkwing.
// The state path is left empty so the caller fills it from its Paths
// (the historical ~/.sparkwing/state.db); the filesystem cache/logs
// paths are expanded by the storeurl factories at open time. This is
// NOT one of BuiltinProfiles() -- laptop is a fallback, not an
// auto-detected profile, so it never participates in detect or default.
func BuiltinLaptopProfile() *Profile {
	return &Profile{
		Name:  "laptop",
		State: &backends.Spec{Type: backends.TypeSQLite},
		Cache: &backends.Spec{Type: backends.TypeFilesystem, Path: "~/.cache/sparkwing"},
		Logs:  &backends.Spec{Type: backends.TypeFilesystem, Path: "~/.cache/sparkwing/logs"},
	}
}

// Resolve picks the active profile. Precedence (highest first):
//
//  1. cliFlag      -- `--profile X` was passed on the command line
//  2. projectHint  -- sparkwing.yaml declares `profile: X`
//  3. detect       -- some profile's `detect:` block matches the env
//  4. default      -- profiles.yaml `default: X`
//  5. builtin      -- synthesized "laptop" profile (sqlite + filesystem)
//
// cliFlag == "" means no flag; projectHint == "" means no hint; file ==
// nil means no profiles.yaml loaded (the builtin laptop still resolves).
//
// Returns ErrProfileNotFound when cliFlag or projectHint names a profile
// absent from file.Profiles; the wrapped message identifies which level
// triggered it. The detect and default levels never error -- a default:
// naming an unknown profile is skipped rather than fatal -- so the
// builtin laptop guarantees a non-nil result with a nil error.
//
// The returned Profile is owned by file (or the synthesized laptop) and
// must not be mutated.
func Resolve(cliFlag, projectHint string, file *Config) (*Profile, Chain, error) {
	profiles := map[string]*Profile{}
	def := ""
	if file != nil {
		if file.Profiles != nil {
			profiles = file.Profiles
		}
		def = file.Default
	}

	if cliFlag != "" {
		if p, ok := profiles[cliFlag]; !ok || p == nil {
			return nil, Chain{}, fmt.Errorf("%w: %q (from --profile)", ErrProfileNotFound, cliFlag)
		}
	}
	if projectHint != "" {
		if p, ok := profiles[projectHint]; !ok || p == nil {
			return nil, Chain{}, fmt.Errorf("%w: %q (from sparkwing.yaml profile:)", ErrProfileNotFound, projectHint)
		}
	}

	defaultName := ""
	if def != "" {
		if p, ok := profiles[def]; ok && p != nil {
			defaultName = def
		}
	}

	levels := []struct {
		source    ChainSource
		name      string
		detectVia string
	}{
		{ChainSourceFlag, cliFlag, ""},
		{ChainSourceProject, projectHint, ""},
		{ChainSourceDefault, defaultName, ""},
		{ChainSourceBuiltin, "laptop", ""},
	}

	winner := -1
	for i, lvl := range levels {
		if lvl.name != "" {
			winner = i
			break
		}
	}

	chain := Chain{
		Selected: levels[winner].name,
		Source:   levels[winner].source,
	}
	for i, lvl := range levels {
		if i == winner {
			continue
		}
		chain.Considered = append(chain.Considered, ConsideredEntry{
			Source: lvl.source,
			Name:   lvl.name,
			Reason: consideredReason(lvl.source, lvl.name, lvl.detectVia, levels[winner], def, i > winner),
		})
	}

	if levels[winner].source == ChainSourceBuiltin {
		return BuiltinLaptopProfile(), chain, nil
	}
	return profiles[chain.Selected], chain, nil
}

func consideredReason(src ChainSource, name, _ string, winner struct {
	source    ChainSource
	name      string
	detectVia string
}, def string, afterWinner bool,
) string {
	if afterWinner {
		if name != "" {
			return fmt.Sprintf("overridden by %s (%s)", winner.source, winner.name)
		}
	}
	switch src {
	case ChainSourceFlag:
		return "no --profile flag passed"
	case ChainSourceProject:
		return "no profile: hint in sparkwing.yaml"
	case ChainSourceDefault:
		if def != "" {
			return fmt.Sprintf("default: %q is not a known profile", def)
		}
		return "no default: set in profiles.yaml"
	default:
		return "not selected"
	}
}
