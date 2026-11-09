//go:build windows

package wingd_test

import "os"

func processFixtureTempRoot() string { return os.TempDir() }

func processFixtureSuffix() string { return ".exe" }
