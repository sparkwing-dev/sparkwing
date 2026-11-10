package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestParentCancelNudgesChildSweepAfterCommit(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	parentBefore, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	response := engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "deploy", parentBefore.Request.Request.Git), 201)
	var child controllerwire.CreateRunResponse
	if err := json.Unmarshal(response.Body.Bytes(), &child); err != nil {
		t.Fatal(err)
	}
	raw, record, err := teststore.ProvisionCredential(t.Context(), e.Store, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "cancel-nudge"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsControl}})
	if err != nil {
		t.Fatal(err)
	}
	childCommitted := make(chan struct{}, 1)
	e.Store.SetRunCancellationObserver(func(_ context.Context, team store.Team, id string) error {
		if team == "a" && id == child.RunID {
			select {
			case childCommitted <- struct{}{}:
			default:
			}
		}
		return nil
	})
	defer e.Store.SetRunCancellationObserver(nil)
	checked := make(chan error, 1)
	nudge := e.cancelCommitted
	e.cancelCommitted = func() {
		checkCtx, stop := context.WithTimeout(t.Context(), time.Second)
		defer stop()
		// safety: A second write proves cancellation released the transaction before waking maintenance.
		row, readErr := tenant.ReadRun(checkCtx, "run")
		if readErr == nil && row.CancelRequestedAt == nil {
			readErr = fmt.Errorf("nudge preceded committed cancel intent")
		}
		if readErr == nil {
			actor := runstate.Actor{Kind: runstate.ActorOperator, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: record.Prefix}, CapabilityPrefix: record.Prefix}
			replay, replayErr := tenant.TransitionRun(checkCtx, runstate.RunTransition{RunID: "run", To: runstate.RunCancelled, Cause: runstate.RunCancel, Actor: actor, Reason: "requested"})
			readErr = replayErr
			if readErr == nil && !replay.Replayed {
				readErr = fmt.Errorf("cancel write was not committed before notification")
			}
		}
		checked <- readErr
		nudge()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	// safety: An hour-long backstop proves the committed cancel wakes maintenance without its ticker.
	go func() { defer close(done); e.Server.runReaper(ctx, time.Hour) }()
	defer func() { cancel(); <-done }()
	bound, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/cancel", `{"reason":"requested"}`, 200)
	select {
	case err := <-checked:
		if err != nil {
			t.Fatal(err)
		}
	case <-bound.Done():
		t.Fatal("committed parent cancellation did not signal reaper")
	}
	select {
	case <-childCommitted:
	case <-bound.Done():
		t.Fatal("child cancellation waited for the ten-second sweep")
	}
	row, err := tenant.ReadRun(t.Context(), child.RunID)
	if err != nil || row.CancelRequestedAt == nil {
		t.Fatalf("child cancellation row=%+v error=%v", row, err)
	}
	parent, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || parent.CancelRequestedAt == nil {
		t.Fatalf("parent cancellation row=%+v error=%v", parent, err)
	}
	if elapsed := row.CancelRequestedAt.Sub(*parent.CancelRequestedAt); elapsed >= 2*time.Second {
		t.Fatalf("child cancellation row lagged committed parent by %s", elapsed)
	}
}

func TestReaperCancelNudgesCoalesce(t *testing.T) {
	server := New(nil, nil)
	for range 1000 {
		server.nudgeReaper()
	}
	if cap(server.reapWake) != 1 || len(server.reapWake) != 1 {
		t.Fatalf("wake queue capacity=%d length=%d", cap(server.reapWake), len(server.reapWake))
	}
	<-server.reapWake
}
