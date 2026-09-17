package orchestrator

import "os"

const envDryRun = "SPARKWING_DRY_RUN"

func dryRunFromEnv() bool {
	return os.Getenv(envDryRun) == "1"
}
