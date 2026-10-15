//go:build unix

package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestEnvelopeRecoveryDeclarations(t *testing.T) {
	for _, shape := range []string{"recover-memo", "recover-skip", "recover-conc"} {
		t.Run(shape, func(t *testing.T) {
			plan := sparkwing.NewPlan()
			if err := (envelopeFixture{}).Plan(t.Context(), plan, envelopeInputs{Shape: shape, Key: "rollback-v1", Marker: t.TempDir()}, sparkwing.RunContext{}); err != nil {
				t.Fatal(err)
			}
			doc := sdkserve.DefaultOperations.Document(plan)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: secrets.NewMasker()}); err != nil {
				t.Fatal(err)
			}
			for _, job := range doc.Jobs {
				if job.ID != "rollback" {
					continue
				}
				if job.OnFailureOf != "deploy" || shape == "recover-memo" && job.Memo == nil || shape == "recover-skip" && len(job.Closures.SkipIf) != 1 || shape == "recover-conc" && len(job.Concurrency) != 1 {
					t.Fatalf("recovery modifiers missing: %+v", job)
				}
				return
			}
			t.Fatalf("recovery missing: %+v", doc)
		})
	}
}

func rollbackBodies(t *testing.T, marker string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(marker, "rollback"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "body")
}

func TestEnvelope_ARecoveryNodeMemoizes(t *testing.T) {
	r := newEnvelopeRig(t, false)
	marker := t.TempDir()
	args := r.args("recover-memo", "key", "rollback-pinned", "marker", marker)
	wantRun(t, r.run("recover-1", "envelope-a", args), "failed")
	wantRun(t, r.run("recover-2", "envelope-a", args), "failed")
	if n := r.node("recover-1", "rollback"); n.Outcome != string(sparkwing.Success) {
		t.Fatalf("first rollback = %s (%s), want success", n.Outcome, n.Error)
	}
	if n := r.node("recover-2", "rollback"); n.Outcome != string(sparkwing.Memoized) {
		t.Fatalf("second rollback = %s (%s), want memoized", n.Outcome, n.Error)
	}
	if got := rollbackBodies(t, marker); got != 1 {
		t.Fatalf("rollback body ran %d times, want once", got)
	}
}

func TestEnvelope_ARecoveryNodeHonorsSkipIf(t *testing.T) {
	r := newEnvelopeRig(t, false)
	marker := t.TempDir()
	wantRun(t, r.run("recover-skip", "envelope-a", r.args("recover-skip", "marker", marker)), "failed")
	if n := r.node("recover-skip", "rollback"); n.Outcome != string(sparkwing.Skipped) {
		t.Fatalf("rollback = %s (%s), want skipped", n.Outcome, n.Error)
	}
	if got := rollbackBodies(t, marker); got != 0 {
		t.Fatalf("rollback body ran %d times, want never", got)
	}
}

func TestEnvelope_ARecoveryNodeTakesAConcurrencySlot(t *testing.T) {
	r := newEnvelopeRig(t, true)
	marker := t.TempDir()
	res := r.start("recover-conc", "envelope-a", r.args("recover-conc", "marker", marker), nil)
	var hold, deploy *gateArrival
	for hold == nil || deploy == nil {
		switch a := r.gate.next(t); a.who {
		case "recover-conc/hold":
			hold = a
		case "recover-conc/deploy":
			deploy = a
		default:
			t.Fatalf("unexpected body %s", a.who)
		}
	}
	deploy.release("fail")
	if who := recv(t, r.gate.deadline, r.daemon.refused, "the rollback refused by its full group"); who != "recover-conc/rollback" {
		t.Fatalf("the daemon refused %s, want the rollback", who)
	}
	hold.release("ok")
	wantRun(t, <-res, "failed")
	if n := r.node("recover-conc", "rollback"); n.Outcome != string(sparkwing.Failed) {
		t.Fatalf("rollback = %s (%s), want failed on the full group", n.Outcome, n.Error)
	}
	if got := rollbackBodies(t, marker); got != 0 {
		t.Fatalf("rollback body ran %d times while the group was full", got)
	}
}
