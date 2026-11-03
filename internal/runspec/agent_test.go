package runspec

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestValidateAgent(t *testing.T) {
	for _, agent := range []string{"", "claude-code", "claude-code:land-agent", "codex", "gemini-cli", "a.b_c-1:2", strings.Repeat("a", MaxAgentLength)} {
		if err := ValidateAgent(agent); err != nil {
			t.Errorf("ValidateAgent(%q) = %v", agent, err)
		}
	}
	for _, agent := range []string{AgentNone, "claude code", ":lead", "-lead", "bad\x00", "bad\nline", "bad\x1b[31m", "é", "a/b", strings.Repeat("a", MaxAgentLength+1)} {
		if err := ValidateAgent(agent); err == nil {
			t.Errorf("ValidateAgent(%q) accepted", agent)
		}
	}
}

func TestAgentIsSealedDisplayedAndCarriedByRetry(t *testing.T) {
	s := fixture(t)
	request := s.Request.Request
	request.Agent = "claude-code:land-agent"
	sealed, err := SealRequest(request, IntakeFacts{RunID: "source", AdmittedAt: time.Unix(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Hash == s.Request.Hash {
		t.Fatal("the agent label is not part of the sealed request hash")
	}
	if got := DisplayRequest(sealed).Agent; got != request.Agent {
		t.Fatalf("display agent = %q", got)
	}
	spec, err := SealSpec(SealInput{Request: sealed, Resolved: s.Resolved, Plan: json.RawMessage(`{"version":2,"jobs":[]}`), Now: time.Unix(2, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if got := DisplaySpec(spec).Agent; got != request.Agent {
		t.Fatalf("spec display agent = %q", got)
	}
	retry, err := CarryRetry(RetryInput{Request: sealed, Spec: Optional[Spec]{Set: true, Value: spec}, Plan: json.RawMessage(`{"version":2,"jobs":[]}`), Retrier: Actor{ID: "retrier"}, SourceRunID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	if retry.Request.Request.Agent != request.Agent {
		t.Fatalf("retry agent = %q", retry.Request.Request.Agent)
	}
	request.Agent = "bad label"
	if _, err := SealRequest(request, IntakeFacts{RunID: "other"}); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("unsafe agent sealed: %v", err)
	}
}
