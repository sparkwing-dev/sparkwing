package store_test

import (
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// B15: log preparation took the run before the writer's authority, while
// renewal takes the authority before the run, so the two deadlocked.
func TestPostgresLogPreparationLocksAuthorityBeforeRunAgainstRenewal(t *testing.T) {
	st, demand, ctx := lockOrderStore(t)
	claim, err := st.AsOperator().ClaimUnit(ctx, demand)
	if err != nil {
		t.Fatal(err)
	}
	prefix := claim.Award.CapabilityPrefix
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, CapabilityPrefix: prefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
	attempt := runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 1, Generation: int64(claim.Award.Generation)}
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &attempt}
	tenant := teststore.ForTeam(t, st, "a")
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET attempt=1 WHERE team='a' AND run_id='run' AND node_id='job'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES ('a','run','job',1,$1,$2,$3,'runner','runner','remote',1)`, claim.Award.Generation, claim.Award.Holder, prefix); err != nil {
		t.Fatal(err)
	}
	if class, err := tenant.PrepareLogWrite(ctx, subject, actor); err != nil || class != "r30" {
		t.Fatalf("uncontended preparation = %q, %v", class, err)
	}

	b := blockRun(t, ctx, st)
	prepared := make(chan error, 1)
	go func() {
		class, err := tenant.PrepareLogWrite(ctx, subject, actor)
		if err == nil && class != "r30" {
			err = fmt.Errorf("retention class %q", class)
		}
		prepared <- err
	}()
	b.waitBlocked(1)
	if !b.held(`SELECT 1 FROM capabilities WHERE prefix='` + prefix + `' FOR UPDATE`) {
		t.Fatal("log preparation waits for the run before locking its writer's authority")
	}
	b.start(func() error { _, err := st.AsOperator().RenewUnit(ctx, prefix, false); return err })
	// safety: renewal waits behind preparation's authority lock, not behind the
	// run, which is the ordering that rules out the cycle.
	b.waitBlocked(2)
	b.release(1)
	if err := <-prepared; err != nil {
		t.Fatalf("log preparation racing renewal: %v", err)
	}
}
