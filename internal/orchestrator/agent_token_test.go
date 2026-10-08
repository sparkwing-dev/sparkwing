package orchestrator

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestTakeAgentTokenHidesItFromChildrenAndMasksIt(t *testing.T) {
	const tok = "agent-token-7f3c9a1e5b2d4c6e8a0b1c2d3e4f5a6b"
	t.Setenv(agentTokenEnv, tok)
	t.Cleanup(func() { agentToken = "" })

	if err := takeAgentToken(); err != nil || agentToken != tok {
		t.Fatalf("takeAgentToken = %v, held %q; want the started-with token", err, agentToken)
	}
	if err := takeAgentToken(); err != nil || agentToken != tok {
		t.Fatalf("a second takeAgentToken = %v, held %q; want the same token", err, agentToken)
	}

	out, err := exec.Command("env").Output()
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	if strings.Contains(string(out), tok) {
		t.Fatalf("a child process inherited %s", agentTokenEnv)
	}

	masker := maskerForInvokeArgs(&sparkwing.Registration{}, nil)
	if got := masker.Mask("bearer " + tok); strings.Contains(got, tok) {
		t.Fatalf("node output not masked: %q", got)
	}
	if _, ok := os.LookupEnv(agentTokenEnv); ok {
		t.Fatalf("%s still set", agentTokenEnv)
	}
}
