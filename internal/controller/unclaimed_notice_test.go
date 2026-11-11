package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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

func TestWaitingNoticeEnrollmentFollowsRepositoryProof(t *testing.T) {
	now := time.Now()
	asserted, err := runspec.AssertedProvenance("account/owner", "refs/heads/main", "0123456789abcdef0123456789abcdef01234567", "ci")
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := asserted.FetchedFrom(1, "acme/repo", "", "")
	if err != nil {
		t.Fatal(err)
	}
	proven, err := runspec.AppProvenance(runspec.AppFacts{Repository: "acme/repo", Event: "push", RepositoryID: 1, DeliveryKey: "delivery", VerifiedAt: now, Ref: "refs/heads/main", SHA: "0123456789abcdef0123456789abcdef01234567"}, runspec.Optional[runspec.PullRequest]{}, "ci")
	if err != nil {
		t.Fatal(err)
	}
	for _, provenance := range []runspec.Provenance{asserted, fetched, proven} {
		for _, cause := range []string{"", "no runner of the allowed tiers is online"} {
			view := store.RunView{Snapshot: store.RunSnapshot{StoreNow: now}, Waiting: &store.UnclaimedUnit{Kind: capability.UnitPlan, Since: now.Add(-time.Minute), NoEligible: cause}}
			view.Snapshot.Run.Request = runspec.SealedRequest{Provenance: provenance, Request: runspec.Request{Dispatch: runspec.DispatchController}}
			view.Snapshot.Run.Request.Request.Options.Requires.Value = []string{"gpu-x"}
			notice := unclaimedNotice(view)
			if notice == nil {
				t.Fatal("missing notice")
			}
			scoped := provenance.ProvenRepository() != ""
			if strings.Contains(notice.Reason, "--repos REPOSITORY") != scoped || strings.Contains(notice.Reason, "--personal") == scoped {
				t.Errorf("repository proof %q: %s", provenance.ProvenRepository(), notice.Reason)
			}
			if !scoped {
				for _, want := range []string{"own manual", "repository-scoped runners cannot run working-tree", "commit and push"} {
					if !strings.Contains(notice.Reason, want) {
						t.Errorf("notice missing %q: %s", want, notice.Reason)
					}
				}
			}
		}
	}
}

func TestRunStatusNamesRepositoryScopedProvenanceRefusal(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Dispatch = runspec.DispatchController
	run.Request.Request.Source.WorkingTree = true
	run.Request.Request.Git.Repository.Display = "https://github.com/acme/repo"
	run.Request.Request.Options.Requires.Value = []string{"gpu-x"}
	body, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET request_json=?,dispatch='controller' WHERE id='run'`, body)
	engineSQL(t, e.Store, `UPDATE triggers SET unit_kind='plan',needs_labels=? WHERE id='run'`, []byte(`["gpu-x"]`))
	_, runner, err := teststore.ProvisionCredential(t.Context(), e.Store, capability.ProvisionSpec{Kind: capability.ProvisionMachine, RegistrationID: "runner:a:gpu", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "gpu"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.TriggersClaim}, AllowedLabels: []string{"gpu-x"}, Repositories: []string{"github.com/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	demand := store.UnitDemand{CapabilityPrefix: runner.Prefix, Target: &store.UnitRef{Kind: capability.UnitPlan, RunID: "run"}, Labels: []string{"gpu-x"}}
	if err := e.Store.AsOperator().RecordRunnerPoll(t.Context(), demand); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.AsOperator().ClaimUnit(t.Context(), demand); err == nil || err.Error() != "repository-scoped runners require repository-proven code" {
		t.Fatalf("claim refusal = %v", err)
	}
	detail, err := e.RunDetail(t.Context(), capability.Authorized{Target: capability.Target{Team: "a", RunID: "run"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Notices) != 1 || !strings.HasPrefix(detail.Notices[0].Reason, "repository-scoped runners require repository-proven code") {
		t.Fatalf("status lost provenance refusal: %+v", detail.Notices)
	}
	t.Log(detail.Notices[0].Reason)
	demand.Labels = nil
	if err := e.Store.AsOperator().RecordRunnerPoll(t.Context(), demand); err != nil {
		t.Fatal(err)
	}
	detail, err = e.RunDetail(t.Context(), capability.Authorized{Target: capability.Target{Team: "a", RunID: "run"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, notice := range detail.Notices {
		if strings.HasPrefix(notice.Reason, "repository-scoped runners require repository-proven code") {
			t.Fatalf("mismatched runner reported as a provenance refusal: %+v", detail.Notices)
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
