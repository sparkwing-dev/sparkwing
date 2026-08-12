package main

import "fmt"

const (
	windowsMoveReplaceExisting = uint32(1)
	windowsMoveWriteThrough    = uint32(8)
)

func replaceWindowsRunningImageWith(source, target string, move func(string, string, uint32) error, remove func(string) error) error {
	old := target + ".old"
	_ = remove(old)
	if err := move(target, old, windowsMoveWriteThrough); err != nil {
		return fmt.Errorf("preserve running binary: %w", err)
	}
	if err := move(source, target, windowsMoveWriteThrough); err != nil {
		if restoreErr := move(old, target, windowsMoveWriteThrough); restoreErr != nil {
			return fmt.Errorf("install new binary: %w; restore running binary: %v", err, restoreErr)
		}
		return fmt.Errorf("install new binary: %w", err)
	}
	return nil
}

func restoreWindowsRunningImageWith(source, target string, move func(string, string, uint32) error, remove func(string) error) error {
	_ = source
	failed := target + ".failed"
	_ = remove(failed)
	if err := move(target, failed, windowsMoveWriteThrough); err != nil {
		return fmt.Errorf("preserve failed binary: %w", err)
	}
	old := target + ".old"
	if err := move(old, target, windowsMoveWriteThrough); err != nil {
		if restoreErr := move(failed, target, windowsMoveWriteThrough); restoreErr != nil {
			return fmt.Errorf("restore running binary: %w; restore failed binary: %v", err, restoreErr)
		}
		return fmt.Errorf("restore running binary: %w", err)
	}
	_ = remove(failed)
	return nil
}
