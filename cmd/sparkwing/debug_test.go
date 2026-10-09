package main

import (
	"testing"
)

func TestClaimToPod(t *testing.T) {
	cases := []struct {
		in      string
		wantPod string
	}{
		{"runner:warm-1", "warm-1"},
		{"pod:run-abc:build", "run-abc:build"},
		{"agent:laptop", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := claimToPod(tc.in); got != tc.wantPod {
			t.Errorf("claimToPod(%q) = %q, want %q", tc.in, got, tc.wantPod)
		}
	}
}

func TestParseDebugTarget_NamespaceOnAttachOnly(t *testing.T) {
	got, err := parseDebugTarget(cmdDebugAttach, []string{"--run", "r1", "--node", "n1"})
	if err != nil || got.namespace != defaultDebugNamespace {
		t.Fatalf("attach default = %+v, %v; want namespace %s", got, err, defaultDebugNamespace)
	}
	got, err = parseDebugTarget(cmdDebugAttach, []string{"--run", "r1", "--node", "n1", "--namespace", "ci"})
	if err != nil || got.namespace != "ci" {
		t.Fatalf("attach --namespace ci = %+v, %v", got, err)
	}
	if _, err := parseDebugTarget(cmdDebugRelease, []string{"--run", "r1", "--node", "n1", "--namespace", "ci"}); err == nil {
		t.Fatal("debug release accepted --namespace")
	}
}

func TestParseDebugTarget_RequiresRunAndNode(t *testing.T) {
	if _, err := parseDebugTarget(cmdDebugRelease, []string{"--run", "r1"}); err == nil {
		t.Fatal("expected error when --node missing")
	}
	if _, err := parseDebugTarget(cmdDebugRelease, []string{"--node", "n1"}); err == nil {
		t.Fatal("expected error when --run missing")
	}
	tgt, err := parseDebugTarget(cmdDebugRelease, []string{"--run", "r1", "--node", "n1"})
	if err != nil {
		t.Fatalf("valid args: %v", err)
	}
	if tgt.run != "r1" || tgt.node != "n1" {
		t.Fatalf("got %+v", tgt)
	}
}
