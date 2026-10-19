package store_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

// B15: log preparation took the run before the writer's authority, while
// renewal takes the authority before the run, so the two deadlocked.
func TestPostgresLogPreparationLocksAuthorityBeforeRunAgainstRenewal(t *testing.T) {
	st, demand, ctx := lockOrderStore(t)
	claim, err := st.ClaimUnit(ctx, demand)
	if err != nil {
		t.Fatal(err)
	}
	prefix := claim.Award.CapabilityPrefix
	actor := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, CapabilityPrefix: prefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
	attempt := runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 1, Generation: claim.Award.Generation}
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &attempt}
	tenant := teststore.ForTeam(t, st, "a")

	b := blockRun(t, ctx, st)
	prepared := make(chan error, 1)
	go func() {
		_, err := tenant.PrepareLogWrite(ctx, subject, actor)
		prepared <- err
	}()
	b.waitBlocked(1)
	if !b.held(`SELECT 1 FROM capabilities WHERE prefix='` + prefix + `' FOR UPDATE`) {
		t.Fatal("log preparation waits for the run before locking its writer's authority")
	}
	b.start(func() error { _, err := st.RenewUnit(ctx, prefix, false); return err })
	b.waitBlocked(2)
	b.release(1)
	var pgErr *pgconn.PgError
	if err := <-prepared; errors.As(err, &pgErr) && pgErr.Code == "40P01" {
		t.Fatalf("log preparation deadlocked with renewal: %v", err)
	}
}
