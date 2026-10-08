package controller_test

import (
	"net/http"
	"strings"
	"testing"
)

// safety: the cut falls after the token-setup subshell, whose own body may hold
// any separator, so a split there never hands the runner half the setup.
func splitConnectCommand(cmd string) (setup, run string, ok bool) {
	head, run, ok := strings.Cut(cmd, ") && ")
	if !ok || !strings.HasPrefix(head, "(") {
		return "", "", false
	}
	return head + ")", run, true
}

func redactToken(cmd string) string {
	_, run, ok := splitConnectCommand(cmd)
	if !ok {
		return "<connect command withheld: no token setup found>"
	}
	return "<token setup> && " + run
}

func TestTheConnectCommandSplitsAfterItsTokenSetup(t *testing.T) {
	f := newIdentityFixture(t)
	a := f.user("a", "alpha@example.com")
	var minted struct {
		Token   string `json:"token"`
		Command string `json:"command"`
	}
	if code := f.call("POST", "/api/v1/team/runner-tokens", a.auth,
		map[string]any{"name": "a-box", "repos": []string{"github.com/acme/*"}}, &minted); code != http.StatusCreated {
		t.Fatalf("mint = %d", code)
	}
	setup, run, ok := splitConnectCommand(minted.Command)
	if !ok || !strings.Contains(setup, minted.Token) || !strings.HasPrefix(run, "sparkwing-runner runner ") {
		t.Fatalf("split %q into setup %q and run %q, want the token setup then the runner", minted.Command, setup, run)
	}
	if logged := redactToken(minted.Command); strings.Contains(logged, minted.Token) {
		t.Fatalf("redacted command %q still carries the token", logged)
	}
}
