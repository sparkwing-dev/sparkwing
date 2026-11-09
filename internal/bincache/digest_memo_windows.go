package bincache

import (
	"os"
	"time"
)

// safety: Windows exposes no change time here, so file digests are never reused.
func fileIdentity(os.FileInfo, time.Time) (string, bool) { return "", false }

func directoryIdentity(os.FileInfo) string { return "" }
