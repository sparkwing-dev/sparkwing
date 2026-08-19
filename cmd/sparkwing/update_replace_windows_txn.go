package main

import "fmt"

const (
	windowsMoveReplaceExisting = uint32(1)
	windowsMoveWriteThrough    = uint32(8)
)

func replaceWindowsRunningImageWith(source, target string, move func(string, string, uint32) error) error {
	if err := move(source, target, windowsMoveReplaceExisting|windowsMoveWriteThrough); err != nil {
		return fmt.Errorf("atomically replace binary: %w", err)
	}
	return nil
}

func restoreWindowsRunningImageWith(source, target string, move func(string, string, uint32) error) error {
	return replaceWindowsRunningImageWith(source, target, move)
}
