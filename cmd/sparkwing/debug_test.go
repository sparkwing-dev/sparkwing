package main

import (
	"testing"
)

// TestClaimToPod exercises the claim-string parser used by
// `sparkwing debug attach` to derive the pod name + namespace.
func TestClaimToPod(t *testing.T) {
	t.Setenv("SPARKWING_NAMESPACE", "test-ns")
	cases := []struct {
		in      string
		wantPod string
		wantNs  string
	}{
		{"runner:warm-1", "warm-1", "test-ns"},
		{"pod:run-abc:build", "run-abc:build", "test-ns"},
		{"agent:laptop", "", "test-ns"},
		{"", "", "test-ns"},
	}
	for _, tc := range cases {
		gotPod, gotNs := claimToPod(tc.in)
		if gotPod != tc.wantPod || gotNs != tc.wantNs {
			t.Errorf("claimToPod(%q) = (%q, %q), want (%q, %q)",
				tc.in, gotPod, gotNs, tc.wantPod, tc.wantNs)
		}
	}
}

// TestParseDebugTarget_RequiresRunAndNode verifies the shared flag
// parser rejects missing --run or --node.
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
