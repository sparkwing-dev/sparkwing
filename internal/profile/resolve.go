package profile

import (
	"fmt"
)

type ChainSource string

const (
	ChainSourceFlag ChainSource = "flag"
	ChainSourceNone ChainSource = "none"

	ChainSourceProjectDefault ChainSource = "project-default"
)

type ConsideredEntry struct {
	Source ChainSource
	Name   string
	Reason string
}

type Chain struct {
	Selected   string
	Source     ChainSource
	Considered []ConsideredEntry
}

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
