package engine

import (
	"os"
	"time"
)

// safety: Windows exposes no change time here, so the executable digest is never reused across processes.
func fileIdentity(os.FileInfo, time.Time) (string, bool) { return "", false }
