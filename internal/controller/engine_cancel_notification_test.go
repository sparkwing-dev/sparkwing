package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestEngineCancellationNotifiesAfterCommitAndRetriesOnReplay(t *testing.T) {
	server, tenant := engineRuntimeStore(t)
	engineSQL(t, server.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('account','a@example.test',1,1)`)
	engineSQL(t, server.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','account','editor',1)`)
	engineSQL(t, server.Store, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES ('session','account','runs.read,runs.control',1,?,'a','account')`, time.Now().Add(time.Hour).UnixNano())
	engineSQL(t, server.Store, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES ('session','a',0)`)
	principal, err := server.Store.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	authorized := capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
	failure := errors.New("admission store unavailable")
	calls := 0
	server.AfterRunCancellation = func(ctx context.Context, team store.Team, runID string) error {
		calls++
		row, err := tenant.ReadRun(ctx, runID)
		if err != nil || team != tenant.Team() || row.CancelRequestedAt == nil || row.StateSeq != 1 {
			t.Fatalf("notification preceded committed intent: team=%s run=%+v error=%v", team, row, err)
		}
		if calls == 1 {
			return failure
		}
		return nil
	}
	if _, err := server.cancel(t.Context(), authorized, controllerwire.CancelRequest{Reason: "user_cancelled"}); err == nil {
		t.Fatal("cancellation hid failed admission notification")
	}
	applied, err := server.cancel(t.Context(), authorized, controllerwire.CancelRequest{Reason: "user_cancelled"})
	if err != nil || !applied.Replayed || calls != 2 {
		t.Fatalf("replayed cancellation did not retry notification: %+v calls=%d error=%v", applied, calls, err)
	}
}
