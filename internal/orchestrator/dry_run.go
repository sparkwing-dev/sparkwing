package orchestrator

import (
	"os"
	"strconv"
)

const envDryRun = "SPARKWING_DRY_RUN"

func dryRunFromEnv() bool {
	enabled, _ := strconv.ParseBool(os.Getenv(envDryRun))
	return enabled
}
