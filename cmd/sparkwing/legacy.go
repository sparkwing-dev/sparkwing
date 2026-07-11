package main

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// homePaths resolves the sparkwing home layout for an explicit home, or
// the default ($SPARKWING_HOME or ~/.sparkwing) when home is empty.
func homePaths(home string) (paths.Paths, error) {
	if home != "" {
		return paths.PathsAt(home), nil
	}
	return paths.DefaultPaths()
}

// liveLegacyBoxSlots reports the box-slot lock markers still held live
// under a sparkwing home. A live marker means an older-pinned pipeline
// binary is admitting outside the daemon: its compiled-in box-slot
// admission runs invisibly to the daemon and can oversubscribe the host
// alongside migrated runs. The fix is to bump that repo's sparkwing pin.
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

// legacyWarningLine renders the one-line coexistence warning shown by
// queue and doctor when older-pinned binaries admit outside the daemon.
// It returns "" when none are live.
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
