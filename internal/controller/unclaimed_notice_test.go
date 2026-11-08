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

func TestPlacementHoldNoticeNamesEachTierAndReplacesTheGenericNotice(t *testing.T) {
	now := time.Now()
	wait := store.PlacementWait{JobID: "macos", Revision: "0123456789abcdef0123456789abcdef01234567", Tiers: []store.PlacementTierWait{
		{Tier: runspec.RepositoryTier{Target: runspec.PlacementTeam, Labels: []string{"os=darwin"}, Hold: 5 * time.Second}, Live: []string{"build-mac"}},
		{Tier: runspec.RepositoryTier{Target: runspec.PlacementGitHubActions, Workflow: ".github/workflows/f.yaml", Runner: "macos-latest"}, OpensIn: 3 * time.Second},
	}}
	view := store.RunView{Snapshot: store.RunSnapshot{StoreNow: now}, Waiting: &store.UnclaimedUnit{Kind: capability.UnitNode, NodeID: "macos", Since: now.Add(-time.Minute)}, PlacementWaits: []store.PlacementWait{wait}}
	view.Snapshot.Run.Request.Request.Dispatch = runspec.DispatchController
	if notice := unclaimedNotice(view); notice != nil {
		t.Fatalf("generic notice duplicated the placement notice: %+v", notice)
	}
	notices := placementNotices(view)
	if len(notices) != 1 {
		t.Fatalf("notices=%+v", notices)
	}
	for _, want := range []string{"job macos waits for placement", "team os=darwin, hold 5s (build-mac live)", "github-actions macos-latest (opens in 3s)"} {
		if !strings.Contains(notices[0].Reason, want) {
			t.Fatalf("reason %q omits %q", notices[0].Reason, want)
		}
	}
	for _, want := range []string{"default branch at 0123456789ab", "Actions minutes"} {
		if !strings.Contains(notices[0].Hint, want) {
			t.Fatalf("hint %q omits %q", notices[0].Hint, want)
		}
	}
}
