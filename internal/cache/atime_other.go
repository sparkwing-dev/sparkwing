//go:build !linux && !darwin

package cache

import (
	"os"
	"time"
)

func lastRead(info os.FileInfo) time.Time { return info.ModTime() }
