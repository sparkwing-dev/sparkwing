//go:build !unix

package local

import (
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
)

func usageFrom(*os.ProcessState) *runner.ResourceUsage { return nil }

func terminationSignal(*os.ProcessState) (os.Signal, bool) { return nil, false }

func isKill(os.Signal) bool { return false }
