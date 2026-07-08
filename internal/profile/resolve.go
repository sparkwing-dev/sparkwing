package profile

import (
	"fmt"
)

// ChainSource tags which resolution rule produced a profile candidate.
type ChainSource string

const (
	ChainSourceFlag ChainSource = "flag" // --profile X
	ChainSourceNone ChainSource = "none" // no --profile passed; project defaults apply
)

// ConsideredEntry is one resolution rule that did not produce the
// selected profile, with a short human-readable reason.
type ConsideredEntry struct {
	Source ChainSource
	Name   string
	Reason string
}

// Chain records the resolution that picked the active profile (or
// reports that none was selected). Selected is empty when no profile
// is active (the no-flag path).
type Chain struct {
	Selected   string
	Source     ChainSource
	Considered []ConsideredEntry
}

// Resolve picks the active profile from cliFlag. There is no
// fallback chain: the project's backends apply when cliFlag is
// empty, and --profile X selects profile X wholesale.
//
// cliFlag == "" returns (nil, Chain{Source: ChainSourceNone}, nil)
// -- no profile is active. cliFlag != "" returns the named profile or
// ErrProfileNotFound when file declares no such profile.
//
// The returned Profile is owned by file and must not be mutated.
func Resolve(cliFlag string, file *Config) (*Profile, Chain, error) {
	if cliFlag == "" {
		return nil, Chain{Source: ChainSourceNone}, nil
	}
	if file == nil || file.Profiles == nil {
		return nil, Chain{}, fmt.Errorf("%w: %q (from --profile); no profiles.yaml loaded", ErrProfileNotFound, cliFlag)
	}
	p, ok := file.Profiles[cliFlag]
	if !ok || p == nil {
		return nil, Chain{}, fmt.Errorf("%w: %q (from --profile)", ErrProfileNotFound, cliFlag)
	}
	return p, Chain{Selected: cliFlag, Source: ChainSourceFlag}, nil
}
