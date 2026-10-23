package profile

import (
	"fmt"
)

type chainSource string

const (
	chainSourceFlag chainSource = "flag"
	chainSourceNone chainSource = "none"
)

type consideredEntry struct {
	Source chainSource
	Name   string
	Reason string
}

type chain struct {
	Selected   string
	Source     chainSource
	Considered []consideredEntry
}

func resolve(cliFlag string, file *config) (*profile, chain, error) {
	if cliFlag == "" {
		return nil, chain{Source: chainSourceNone}, nil
	}
	if file == nil || file.Profiles == nil {
		return nil, chain{}, fmt.Errorf("%w: %q (from --profile); no profiles loaded", errProfileNotFound, cliFlag)
	}
	p, ok := file.Profiles[cliFlag]
	if !ok || p == nil {
		return nil, chain{}, fmt.Errorf("%w: %q (from --profile)", errProfileNotFound, cliFlag)
	}
	return p, chain{Selected: cliFlag, Source: chainSourceFlag}, nil
}
