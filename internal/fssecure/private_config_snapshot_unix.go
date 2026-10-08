//go:build !windows

package fssecure

import "os"

func privateConfigSnapshot(path string) (os.FileInfo, error) { return os.Lstat(path) }
