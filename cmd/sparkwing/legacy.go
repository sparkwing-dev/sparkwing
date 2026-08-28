package main

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

func homePaths(home string) (paths.Paths, error) {
	if home != "" {
		return paths.PathsAt(home), nil
	}
	return paths.DefaultPaths()
}

func liveLegacyBoxSlots(home string) ([]boxslot.Holder, error) {
	p, err := homePaths(home)
	if err != nil {
		return nil, err
	}
	holders, err := boxslot.Holders(p.BoxSlotDir())
	if err != nil {
		return nil, err
	}
	var live []boxslot.Holder
	for _, h := range holders {
		if h.Live {
			live = append(live, h)
		}
	}
	return live, nil
}

func legacyWarningLine(n int) string {
	if n <= 0 {
		return ""
	}
	noun := "pipeline"
	if n != 1 {
		noun = "pipelines"
	}
	return fmt.Sprintf(
		"%d legacy-pinned %s running outside daemon admission -- bump their sparkwing pins",
		n, noun)
}
