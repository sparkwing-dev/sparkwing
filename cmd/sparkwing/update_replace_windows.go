//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func atomicReplace(source, target string) error {
	return replaceWindowsRunningImage(source, target)
}

func atomicRestore(source, target string) error {
	return restoreWindowsRunningImageWith(source, target, windowsMoveFileEx, os.Remove)
}

func replaceWindowsRunningImage(source, target string) error {
	return replaceWindowsRunningImageWith(source, target, windowsMoveFileEx, os.Remove)
}

func windowsMoveFileEx(source, target string, flags uint32) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourcePtr, targetPtr, flags)
}

func syncDir(string) error { return nil }
