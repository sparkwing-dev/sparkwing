package main

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const agentEnv = "SPARKWING_AGENT"

func resolveRunAgent(flag string, flagSet bool, getenv func(string) string) (string, error) {
	agent, source := flag, "--agent"
	if !flagSet {
		agent, source = getenv(agentEnv), agentEnv
		if agent == "" {
			return detectAgent(getenv), nil
		}
	}
	if agent == runspec.AgentNone {
		return "", nil
	}
	if err := runspec.ValidateAgent(agent); err != nil {
		return "", fmt.Errorf("%s: %w", source, err)
	}
	return agent, nil
}

func detectAgent(getenv func(string) string) string {
	// safety: an agent started by another inherits its parent's variables, so agents Claude Code launches come first.
	switch {
	case getenv("CODEX_THREAD_ID") != "":
		return "codex"
	case getenv("GEMINI_CLI") == "1":
		return "gemini-cli"
	case getenv("CLAUDECODE") == "1":
		return "claude-code"
	}
	return ""
}
