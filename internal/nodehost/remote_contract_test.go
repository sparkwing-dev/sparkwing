package nodehost

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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
