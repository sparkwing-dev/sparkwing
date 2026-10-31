package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestAssertedPoolWaitingNoticeExplainsVerifiedDefaultRemedy(t *testing.T) {
	now := time.Now()
	view := store.RunView{Snapshot: store.RunSnapshot{StoreNow: now}, Waiting: &store.UnclaimedUnit{Kind: capability.UnitPlan, Since: now.Add(-time.Minute)}}
	provenance, err := runspec.AssertedProvenance("account/owner", "refs/heads/main", "0123456789abcdef0123456789abcdef01234567", "ci")
	if err != nil {
		t.Fatal(err)
	}
	view.Snapshot.Run.Request = runspec.SealedRequest{Request: runspec.Request{Dispatch: runspec.DispatchController, Options: runspec.Options{Requires: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"pool=deploy"}}}}}, Provenance: provenance}
	notice := unclaimedNotice(view)
	if notice == nil {
		t.Fatal("missing waiting notice")
	}
	for _, want := range []string{"reserved", "verified default-branch work", "push", "default branch", "GitHub App", "pool without reserved secrets"} {
		if !strings.Contains(notice.Reason, want) {
			t.Errorf("notice = %q, want %q", notice.Reason, want)
		}
	}
}

func TestWaitingJobNoticeNamesRequiredLabelAndEnrollmentCommand(t *testing.T) {
	for _, cause := range []string{"", "no runner of the allowed tiers is online"} {
		now := time.Now()
		node := runstate.NodeRow{Ref: runspec.NodeRef{NodeID: "greet"}}
		node.Execution.Requires.Set = true
		node.Execution.Requires.Value = []string{"gpu-x"}
		view := store.RunView{
			Snapshot: store.RunSnapshot{StoreNow: now, Nodes: []runstate.NodeRow{node}},
			Waiting:  &store.UnclaimedUnit{Kind: capability.UnitNode, NodeID: "greet", Since: now.Add(-time.Minute), NoEligible: cause},
		}
		view.Snapshot.Run.Request.Request.Dispatch = runspec.DispatchController
		notice := unclaimedNotice(view)
		if notice == nil {
			t.Fatal("missing waiting notice")
		}
		for _, want := range []string{"gpu-x", "sparkwing runners add", "--label 'gpu-x'", "--session-stdin"} {
			if !strings.Contains(notice.Reason, want) {
				t.Errorf("notice = %q; missing %q", notice.Reason, want)
			}
		}
	}
}
