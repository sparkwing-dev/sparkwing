package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// installVerifiedAsset isolates the existing stage-and-replace behavior. The
// updater does not call this seam until its transaction contract is complete.
func installVerifiedAsset(asset verifiedReleaseAsset, currentBin string) error {
	stagedBin := currentBin + ".update.tmp"
	if err := os.WriteFile(stagedBin, asset.bytes, 0o755); err != nil {
		return fmt.Errorf("stage new binary: %w", err)
	}
	if runtime.GOOS == "darwin" {
		_ = exec.Command("codesign", "--force", "--sign", "-", stagedBin).Run()
	}
	if err := replaceRunningBinary(stagedBin, currentBin); err != nil {
		_ = os.Remove(stagedBin)
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}
