package limits

import "time"

const (
	// GitHubRunnerCredentialTTL bounds GitHub runner credential lifetime.
	GitHubRunnerCredentialTTL time.Duration = time.Hour
)
