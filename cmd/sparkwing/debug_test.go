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

func TestDebugResumeRequiresRunAndJob(t *testing.T) {
	for _, args := range [][]string{{"debug", "resume", "--run=r1"}, {"debug", "resume", "--job=n1"}} {
		if _, err := clisurface.Parse("sparkwing", args); err == nil {
			t.Fatal("accepted an incomplete selector")
		}
	}
	inv, err := clisurface.Parse("sparkwing", []string{"debug", "resume", "--run=r1", "--job=n1"})
	if err != nil || inv.Value("run") != "r1" || inv.Value("job") != "n1" {
		t.Fatalf("inv=%+v err=%v", inv, err)
	}
}
