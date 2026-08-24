//go:build !unix

package local

import (
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
)

// Windows exposes neither rusage nor a termination signal, so a node
// process there is judged on its exit code alone. That is the same
// degradation the platform already carries for CPU sampling.
func usageFrom(*os.ProcessState) *runner.ResourceUsage { return nil }

func terminationSignal(*os.ProcessState) (os.Signal, bool) { return nil, false }

func isKill(os.Signal) bool { return false }
