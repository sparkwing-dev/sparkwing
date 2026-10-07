package orchestrator

import "os"

const agentTokenEnv = "SPARKWING_AGENT_TOKEN"

var agentToken string

// safety: the pipeline binary runs the team's code, and every command it starts
// inherits the environment, so the bearer leaves it here and is masked in output.
// Engine children that need it get it explicitly from their runner config.
func takeAgentToken() string {
	if tok, ok := os.LookupEnv(agentTokenEnv); ok {
		agentToken = tok
		_ = os.Unsetenv(agentTokenEnv)
	}
	return agentToken
}
