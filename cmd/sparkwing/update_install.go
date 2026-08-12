package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	updateReplace      = atomicReplace
	updateRestore      = atomicRestore
	updateMutateStaged = func(string) {}
	afterInstallHook   = func(string) {}
	updateSyncDir      = syncDir
)

func installVerifiedAsset(asset verifiedReleaseAsset, currentBin string) error {
	dir := filepath.Dir(currentBin)
	stage, err := writeInstallTemp(dir, ".sparkwing-update-*", asset.bytes, 0o755)
	if err != nil {
		return fmt.Errorf("stage verified binary: %w", err)
	}
	stageOwned := true
	defer func() {
		if stageOwned {
			_ = os.Remove(stage)
		}
	}()
	updateMutateStaged(stage)
	stagedDigest, err := sha256OfFile(stage)
	if err != nil {
		return fmt.Errorf("rehash staged binary: %w", err)
	}
	if stagedDigest != asset.digest {
		return fmt.Errorf("staged binary digest mismatch: got %s want %s", stagedDigest, asset.digest)
	}

	oldBody, err := os.ReadFile(currentBin)
	if err != nil {
		return fmt.Errorf("read installed binary for rollback: %w", err)
	}
	backup, err := writeInstallTemp(dir, ".sparkwing-rollback-*", oldBody, 0o755)
	if err != nil {
		return fmt.Errorf("stage rollback binary: %w", err)
	}
	backupOwned := true
	defer func() {
		if backupOwned {
			_ = os.Remove(backup)
		}
	}()
	oldDigest, err := sha256OfFile(backup)
	if err != nil {
		return fmt.Errorf("hash rollback binary: %w", err)
	}

	if err := updateReplace(stage, currentBin); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	stageOwned = false
	afterInstallHook(currentBin)
	installedDigest, hashErr := sha256OfFile(currentBin)
	if hashErr == nil && installedDigest == asset.digest {
		if syncErr := updateSyncDir(dir); syncErr == nil {
			return nil
		} else {
			hashErr = fmt.Errorf("sync installed binary directory: %w", syncErr)
		}
	}

	primary := fmt.Errorf("installed binary digest mismatch: got %s want %s", installedDigest, asset.digest)
	if hashErr != nil {
		primary = fmt.Errorf("rehash installed binary: %w", hashErr)
	}
	if err := updateRestore(backup, currentBin); err != nil {
		backupOwned = false
		return errors.Join(primary, fmt.Errorf("restore rollback binary from %s: %w", backup, err))
	}
	backupOwned = false
	if err := updateSyncDir(dir); err != nil {
		return errors.Join(primary, fmt.Errorf("sync restored binary directory: %w", err))
	}
	restoredDigest, err := sha256OfFile(currentBin)
	if err != nil {
		return errors.Join(primary, fmt.Errorf("rehash restored binary: %w", err))
	}
	if restoredDigest != oldDigest {
		return errors.Join(primary, fmt.Errorf("restored binary digest mismatch: got %s want %s", restoredDigest, oldDigest))
	}
	return primary
}

func writeInstallTemp(dir, pattern string, body []byte, mode os.FileMode) (path string, err error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path = file.Name()
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return "", err
	}
	if _, err := file.Write(body); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}
