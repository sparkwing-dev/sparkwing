package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

var (
	updateReplace      = replaceRunningBinary
	updateMutateStaged = func(path string) {
		if runtime.GOOS == "darwin" {
			_ = exec.Command("codesign", "--force", "--sign", "-", path).Run()
		}
	}
)

// installVerifiedAsset isolates the existing stage-and-replace behavior. The
// updater does not call this seam until its transaction contract is complete.
func installVerifiedAsset(asset verifiedReleaseAsset, currentBin string) error {
	stagedBin := currentBin + ".update.tmp"
	if err := os.WriteFile(stagedBin, asset.bytes, 0o755); err != nil {
		return fmt.Errorf("stage new binary: %w", err)
	}
	updateMutateStaged(stagedBin)
	if err := updateReplace(stagedBin, currentBin); err != nil {
		_ = os.Remove(stagedBin)
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}
