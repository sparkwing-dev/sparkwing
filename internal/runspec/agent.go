package runspec

import "fmt"

// MaxAgentLength bounds a run's agent label.
const MaxAgentLength = 64

// AgentNone is the value that opts a run out of an agent label.
const AgentNone = "none"

// ValidateAgent accepts an empty label or one of at most MaxAgentLength letters, digits,
// '.', '_', '-' and ':' that starts with a letter or digit, such as claude-code:land-agent.
// AgentNone is refused because it means "no label" and is never recorded.
func ValidateAgent(agent string) error {
	if agent == "" {
		return nil
	}
	if agent == AgentNone {
		return fmt.Errorf("agent %q means no agent; omit the label instead", AgentNone)
	}
	if len(agent) > MaxAgentLength {
		return fmt.Errorf("agent label is %d bytes; the limit is %d", len(agent), MaxAgentLength)
	}
	for i, c := range []byte(agent) {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '.' && c != '_' && c != '-' && c != ':') {
			return fmt.Errorf("agent label %q must start with a letter or digit and use only letters, digits, '.', '_', '-' and ':'", agent)
		}
	}
	return nil
}
