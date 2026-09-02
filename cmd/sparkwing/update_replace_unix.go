//go:build !windows

package main

import "os"

func atomicReplace(source, target string) error {
	// #nosec G703 -- paths the updater staged in its own install directory
	return os.Rename(source, target)
}

func atomicRestore(source, target string) error {
	return atomicReplace(source, target)
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
