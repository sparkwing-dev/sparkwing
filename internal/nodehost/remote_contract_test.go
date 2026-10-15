package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type ownerWireFixture struct {
	controllerwire.Controller
	snapshot controllerwire.ScheduleSnapshot
	err      error
}

func (f ownerWireFixture) ReadSnapshot(context.Context, string) (controllerwire.ScheduleSnapshot, error) {
	return f.snapshot, f.err
}

func TestRemoteOwnerSchedulePreservesRecoveryAndEmptyGroups(t *testing.T) {
	wire := ownerWireFixture{snapshot: controllerwire.ScheduleSnapshot{
		RunID: "run", CancelRequested: true,
		Jobs: []controllerwire.ScheduleJob{{
			JobDetail:   controllerwire.JobDetail{ID: "rollback", Status: "pending", Generation: 7, Expansion: "expected"},
			Declaration: nodeproto.JobSpec{ID: "rollback", Needs: []string{"source", "prepare"}, NeedsIfPresent: []string{"optional"}, NeedsGenerated: []string{"empty"}, OnFailureOf: "source", ContinueOnError: true, Optional: true},
			Ready:       true, Claimed: true,
		}},
		Generators: []controllerwire.ScheduleGenerator{{ID: "empty", SourceJob: "source", State: "accepted", Members: []string{}}},
	}}
	got, err := (&RemoteOwnerStore{Controller: wire}).Schedule(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	want := store.ScheduleSnapshot{CancelRequested: true, Nodes: []store.ScheduleNode{{
		ID: "rollback", Kind: "work", Status: "pending", Generation: 7, Ready: true, Claimed: true,
		Deps: []string{"source", "prepare"}, DepsIfPresent: []string{"optional"}, NeedsGenerated: []string{"empty"},
		OnFailureOf: "source", ContinueOnError: true, Optional: true, Expansion: "expected",
	}}, Expansions: []store.ScheduleExpansion{{GeneratorID: "empty", SourceNodeID: "source", State: "accepted", Members: []string{}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schedule = %#v; want %#v", got, want)
	}
	got.Nodes[0].Deps[0] = "changed"
	if wire.snapshot.Jobs[0].Declaration.Needs[0] != "source" {
		t.Fatal("projection aliases transport data")
	}
}

func TestRemoteOwnerScheduleRejectsMismatchedIdentity(t *testing.T) {
	for _, snapshot := range []controllerwire.ScheduleSnapshot{
		{RunID: "other"},
		{RunID: "run", Jobs: []controllerwire.ScheduleJob{{JobDetail: controllerwire.JobDetail{ID: "one"}, Declaration: nodeproto.JobSpec{ID: "two"}}}},
	} {
		if _, err := (&RemoteOwnerStore{Controller: ownerWireFixture{snapshot: snapshot}}).Schedule(t.Context(), "run"); err == nil {
			t.Fatal("accepted mismatched snapshot")
		}
	}
}

func TestRemoteOwnerSchedulePropagatesTransportErrors(t *testing.T) {
	sentinel := errors.New("transport failed")
	wire := ownerWireFixture{err: sentinel}
	if _, err := (&RemoteOwnerStore{Controller: wire}).Schedule(t.Context(), "run"); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
}

type ownerOperationWire struct {
	controllerwire.Controller
	reconcile  controllerwire.ReconcileResponse
	expansion  controllerwire.ExpansionAccepted
	request    controllerwire.ReconcileRequest
	submission controllerwire.ExpansionSubmission
	run, job   string
	err        error
}

func (f *ownerOperationWire) ReconcileJob(_ context.Context, run, job string, request controllerwire.ReconcileRequest) (controllerwire.ReconcileResponse, error) {
	f.run, f.job, f.request = run, job, request
	return f.reconcile, f.err
}
func (f *ownerOperationWire) AcceptExpansion(_ context.Context, run, job string, request controllerwire.ExpansionSubmission) (controllerwire.ExpansionAccepted, error) {
	f.run, f.job, f.submission = run, job, request
	return f.expansion, f.err
}
func executionWireFixture() controllerwire.ExecutionJob {
	now := time.Unix(1234, 0)
	declaration := nodeproto.JobSpec{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}
	spec, _ := json.Marshal(declaration)
	return controllerwire.ExecutionJob{
		JobDetail: controllerwire.JobDetail{ID: "job", Status: "pending", Generation: 4, Attempt: 0, StateSeq: 11, Expansion: "expected", StartedAt: &now},
		Spec:      spec, SpecHash: "job-hash", Execution: runspec.JobExecutionSettings{Retry: runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: 3}}},
		ApplicationRetries: 2, Disruptions: 1, ReadyAt: &now, LeaseExpiresAt: &now, ClaimedBy: "holder", CapabilityPrefix: "claim-prefix", ExpansionDigest: "accepted-body",
	}
}
func TestRemoteReconciliationRetainsPrivateAwardAndExecutionState(t *testing.T) {
	job := executionWireFixture()
	f := &ownerOperationWire{reconcile: controllerwire.ReconcileResponse{Kind: string(store.Execute), Job: job,
		Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, RunID: "run", JobID: "job", Capability: "private-award", LeaseExpiresAt: *job.LeaseExpiresAt,
			Binding: capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "holder", Generation: 4}}}}
	source := &store.MaterializeSource{Kind: store.SourceMemo, Source: runspec.AttemptIdentity{RunID: "previous", NodeID: "source", Attempt: 7, Generation: 2}}
	request := store.ReconcileRequest{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Policy: store.SourceMemo, Source: source}
	got, err := (&RemoteOwnerStore{Controller: f}).ReconcileNode(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Bearer) != "private-award" || got.Node.ApplicationRetries != 2 || got.Node.Disruptions != 1 || got.Node.StateSeq != 11 || got.Node.ExpansionDigest != "accepted-body" || got.Node.Attempt != 0 || !reflect.DeepEqual(got.Node.Execution, job.Execution) || !bytes.Equal(got.Node.Spec, job.Spec) {
		t.Fatalf("lost execution state: %+v", got)
	}
	if f.run != "run" || f.job != "job" || f.request.Source.Attempt != source.Source || f.request.Policy != "memo" {
		t.Fatal("changed reconciliation request")
	}
	originalResponse := f.reconcile
	for _, mutation := range []string{"missing bearer", "wrong generation", "wrong ordinal", "wrong job", "wrong lease", "probe awards"} {
		t.Run(mutation, func(t *testing.T) {
			changed := originalResponse
			claim := *changed.Claim
			changed.Claim = &claim
			probe := request
			switch mutation {
			case "missing bearer":
				claim.Capability = ""
			case "wrong generation":
				claim.Binding.Generation++
			case "wrong ordinal":
				claim.Binding.Attempt++
			case "wrong job":
				claim.Binding.NodeID = "other"
			case "wrong lease":
				claim.LeaseExpiresAt = claim.LeaseExpiresAt.Add(time.Second)
			case "probe awards":
				probe.MaterializeOnly = true
			}
			f.reconcile = changed
			if _, err := (&RemoteOwnerStore{Controller: f}).ReconcileNode(t.Context(), probe); err == nil {
				t.Fatal("accepted inconsistent execution award")
			}
		})
	}
}
func TestRemoteReconciliationPreservesNonexecutingResults(t *testing.T) {
	for _, kind := range []store.ReconcileKind{store.KeepTerminal, store.Materialize, store.WaitHolder, store.Dispatch} {
		t.Run(string(kind), func(t *testing.T) {
			job := executionWireFixture()
			if kind == store.KeepTerminal || kind == store.Materialize {
				job.Status, job.Outcome = "done", "memoized"
			}
			if kind == store.Dispatch {
				job.ClaimedBy = ""
				job.CapabilityPrefix = ""
				job.LeaseExpiresAt = nil
			}
			f := &ownerOperationWire{reconcile: controllerwire.ReconcileResponse{Kind: string(kind), Job: job}}
			got, err := (&RemoteOwnerStore{Controller: f}).ReconcileNode(t.Context(), store.ReconcileRequest{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, MaterializeOnly: true})
			if err != nil || got.Kind != kind || got.Claim != nil || len(got.Bearer) != 0 || !f.request.MaterializeOnly {
				t.Fatalf("result=%+v err=%v", got, err)
			}
		})
	}
}
func TestRemoteExpansionReplayRetainsCurrentJobStateAndEmptyBody(t *testing.T) {
	job := executionWireFixture()
	job.Status, job.Outcome = "done", "success"
	var declaration nodeproto.JobSpec
	if err := json.Unmarshal(job.Spec, &declaration); err != nil {
		t.Fatal(err)
	}
	f := &ownerOperationWire{expansion: controllerwire.ExpansionAccepted{SourceGeneration: 2, State: "accepted", BodyHash: "hash", Replayed: true,
		Jobs: []controllerwire.ExpandedJob{{Job: declaration, SpecHash: job.SpecHash, Execution: job.Execution, Current: job}}}}
	submission := store.ExpansionSubmission{Source: store.NodeRef{RunID: "run", NodeID: "source"}, SourceGeneration: 2, Body: json.RawMessage(`{"jobs":[]}`)}
	got, err := (&RemoteOwnerStore{Controller: f}).AcceptExpansion(t.Context(), submission)
	if err != nil || !got.Replayed || len(got.Nodes) != 1 || got.Nodes[0].Status != store.StatusDone || got.Nodes[0].Outcome != store.NodeSuccess || got.Nodes[0].StateSeq != 11 || !bytes.Equal(f.submission.Body, submission.Body) {
		t.Fatalf("expansion reset accepted state: %+v, %v", got, err)
	}
	f.expansion.Jobs = nil
	got, err = (&RemoteOwnerStore{Controller: f}).AcceptExpansion(t.Context(), submission)
	if err != nil || got.State != store.ExpansionAccepted || len(got.Nodes) != 0 {
		t.Fatalf("empty expansion: %+v, %v", got, err)
	}
	f.expansion.SourceGeneration++
	if _, err := (&RemoteOwnerStore{Controller: f}).AcceptExpansion(t.Context(), submission); err == nil {
		t.Fatal("accepted changed source fence")
	}
}
func TestRemoteOperationsPreserveTransportErrors(t *testing.T) {
	sentinel := errors.New("controller unavailable")
	f := &ownerOperationWire{err: sentinel}
	remote := &RemoteOwnerStore{Controller: f}
	if _, err := remote.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: store.NodeRef{RunID: "r", NodeID: "n"}}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if _, err := remote.AcceptExpansion(t.Context(), store.ExpansionSubmission{Source: store.NodeRef{RunID: "r", NodeID: "n"}, Failure: &store.ExpansionFailure{Code: apierr.SDKError, MaskedError: "masked"}}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if f.submission.Failure == nil || f.submission.Failure.Message != "masked" {
		t.Fatal("lost structured expansion failure")
	}
}

func TestRemoteReconciliationRefusesUnrepresentableResultSource(t *testing.T) {
	f := &ownerOperationWire{}
	_, err := (&RemoteOwnerStore{Controller: f}).ReconcileNode(t.Context(), store.ReconcileRequest{
		Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Policy: store.SourceManualRetry,
		Source: &store.MaterializeSource{Kind: store.SourceManualRetry, Result: &store.MaterializedResultSource{Ref: store.NodeRef{RunID: "previous", NodeID: "job"}, StateSeq: 9}},
	})
	if err == nil || f.run != "" {
		t.Fatal("sent a result source after silently dropping its identity")
	}
}
