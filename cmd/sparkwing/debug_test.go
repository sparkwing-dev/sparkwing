package main

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestDebugNamespaceBelongsOnlyToAttach(t *testing.T) {
	for _, verb := range []string{"attach", "env", "resume"} {
		inv, err := clisurface.Parse("sparkwing", []string{"debug", verb, "--run=r1", "--job=n1", "--namespace=ci"})
		if verb == "attach" {
			if err != nil || inv.Value("namespace") != "ci" {
				t.Fatalf("attachment namespace: %+v %v", inv, err)
			}
		} else if err == nil {
			t.Fatalf("debug %s accepted --namespace", verb)
		}
	}
}

func TestParseDebugTarget_RequiresRunAndJob(t *testing.T) {
	if _, err := parseDebugTarget(cmdDebugRelease, []string{"--run", "r1"}); err == nil {
		t.Fatal("expected error when --job missing")
	}
	if _, err := parseDebugTarget(cmdDebugRelease, []string{"--job", "n1"}); err == nil {
		t.Fatal("expected error when --run missing")
	}
	tgt, err := parseDebugTarget(cmdDebugRelease, []string{"--run", "r1", "--job", "n1"})
	if err != nil {
		t.Fatalf("valid args: %v", err)
	}
	if tgt.run != "r1" || tgt.node != "n1" {
		t.Fatalf("got %+v", tgt)
	}
}
