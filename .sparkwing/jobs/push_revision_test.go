package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestPrePushRefusesUncheckedMainSource(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "main")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	head, err := gitIn(root)(newTestContext(t), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	topic := strings.Repeat("b", 40)
	zero := strings.Repeat("0", 40)
	for _, refs := range []string{
		"refs/heads/topic " + topic + " refs/heads/main " + head,
		"refs/heads/main " + head + " refs/heads/backup " + zero + "\nrefs/heads/topic " + topic + " refs/heads/main " + head,
		"(delete) " + zero + " refs/heads/old " + topic + "\nrefs/heads/topic " + topic + " refs/heads/main " + head,
		"refs/heads/main " + head + " refs/heads/main " + zero + "\nrefs/heads/topic " + topic + " refs/heads/main " + head,
	} {
		t.Run(refs, func(t *testing.T) {
			t.Setenv("SPARKWING_GATE_PUSH_REFS", refs)
			host, plan, _ := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
				return (&PrePush{}).Plan(ctx, plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-push"})
			})
			_, err := host.RunStep(t.Context(), plan, "affected-tests", "test")
			if err == nil || !strings.Contains(err.Error(), "check out") || !strings.Contains(err.Error(), topic) || !strings.Contains(err.Error(), head) {
				t.Fatalf("main update from unchecked topic: %v; want refusal naming both OIDs and checkout remedy", err)
			}
		})
	}
}

func TestPrePushDeletionDoesNotRunAffectedTests(t *testing.T) {
	t.Setenv("SPARKWING_GATE_PUSH_REFS", "(delete) "+strings.Repeat("0", 40)+" refs/heads/main "+strings.Repeat("a", 40))
	doc := inspectPlan(t, func(plan *sparkwing.Plan) error {
		return (&PrePush{}).Plan(t.Context(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-push"})
	})
	for _, job := range doc.Jobs {
		if job.ID == "affected-tests" {
			t.Fatal("deleting main tests an unrelated HEAD")
		}
	}
}

func TestDeliveryDocumentsPushAndMergeSafety(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(sourceTreeRoot(), "DELIVERY.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"checked-out HEAD", "Deletions add no", "affected tests once", "qatrust", "remote OID", "full critical set"} {
		if !strings.Contains(string(body), phrase) || !strings.Contains((PrePush{}).Help()+(Land{}).Help(), phrase) {
			t.Errorf("delivery docs and pipeline help must describe %q", phrase)
		}
	}
}

func TestPrePushAcceptsCheckedMainSource(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "main")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	head, err := gitIn(root)(newTestContext(t), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_GATE_PUSH_REFS", "refs/heads/topic "+head+" refs/heads/main "+head)
	host, plan, _ := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (&PrePush{}).Plan(ctx, plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-push"})
	})
	if _, err := host.RunStep(t.Context(), plan, "affected-tests", "test"); err != nil {
		t.Fatal(err)
	}
}
