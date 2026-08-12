//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func atomicReplace(source, target string) error {
	return replaceWindowsRunningImage(source, target)
}

func replaceWindowsRunningImage(source, target string) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_REPLACE_EXISTING) | uint32(windows.MOVEFILE_WRITE_THROUGH)
	if err := windows.MoveFileEx(sourcePtr, targetPtr, flags); err == nil {
		return nil
	}
	old := target + ".old"
	oldPtr, err := windows.UTF16PtrFromString(old)
	if err != nil {
		return err
	}
	_ = os.Remove(old)
	if err := windows.MoveFileEx(targetPtr, oldPtr, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("preserve running binary: %w", err)
	}
	if err := windows.MoveFileEx(sourcePtr, targetPtr, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		if restoreErr := windows.MoveFileEx(oldPtr, targetPtr, windows.MOVEFILE_WRITE_THROUGH); restoreErr != nil {
			return fmt.Errorf("install new binary: %w; restore running binary: %v", err, restoreErr)
		}
		return fmt.Errorf("install new binary: %w", err)
	}
	return nil
}

func syncDir(string) error { return nil }
