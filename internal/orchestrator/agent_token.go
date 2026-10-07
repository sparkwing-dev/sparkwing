package orchestrator

import (
	"fmt"
	"os"
)

const agentTokenEnv = "SPARKWING_AGENT_TOKEN"

// safety: Main moves the bearer here before any verb runs; verbs read it, never the environment.
var agentToken string

// safety: the pipeline binary runs the team's code, and every command it starts
// inherits the environment, so the bearer leaves it here and is masked in output.
// Engine children that need it get it explicitly from their runner config.
func takeAgentToken() error {
	tok, ok := os.LookupEnv(agentTokenEnv)
	if !ok {
		return nil
	}
	agentToken = tok
	if err := os.Unsetenv(agentTokenEnv); err != nil {
		return fmt.Errorf("remove %s from the environment: %w", agentTokenEnv, err)
	}
	return nil
}
