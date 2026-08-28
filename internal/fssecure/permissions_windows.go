//go:build windows

package fssecure

import (
	"io/fs"
	"os"
)

const auditSupported = false

func tighten(string, fs.FileMode) error { return nil }

func tightenOpen(*os.File, fs.FileMode) error { return nil }

func repairTree(string, os.FileInfo, bool) ([]Change, error) { return nil, nil }
