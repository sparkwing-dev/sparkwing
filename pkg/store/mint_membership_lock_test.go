package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// Account deletion drops its memberships before it sweeps the account's
// tokens. A mint that reads its minter's role while that drop is uncommitted
// waits for it and then finds no role, rather than reading the old row and
// landing a token after the sweep. The open transaction below holds the drop
// at that point; SQLite serializes writers, so only Postgres can interleave.
func TestTokenMintWaitsOnAnUncommittedMembershipDrop(t *testing.T) {
	mints := map[string]func(*store.Tenant, string) (string, error){
		"cli": func(tn *store.Tenant, account string) (string, error) {
			raw, _, err := tn.CreateCLIToken(context.Background(), "cli:"+account, []string{"runs.read"}, account, time.Now())
			return raw, err
		},
		"runner": func(tn *store.Tenant, account string) (string, error) {
			raw, _, err := tn.CreateRunnerToken(context.Background(), "agent:"+account, []string{"nodes.claim"}, account, time.Now())
			return raw, err
		},
	}
	for name, mint := range mints {
		t.Run(name, func(t *testing.T) {
			st := storetest.OpenPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			owner := signIn(t, st, "owner-"+name, "owner-"+name+"@example.com")
			member := signIn(t, st, "member-"+name, "member-"+name+"@example.com")
			tn := tenant(t, st, owner.PersonalTeam)
			inv, err := tn.CreateInvitation(ctx, owner.Account.ID, member.Account.Email, store.RoleEditor, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.AcceptInvitation(ctx, member.Account.ID, inv.ID, time.Now()); err != nil {
				t.Fatal(err)
			}

			drop, err := st.DB().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = drop.Rollback() }()
			if _, err := drop.ExecContext(ctx, storetest.Rebind(st,
				`DELETE FROM memberships WHERE account_id = ?`), member.Account.ID); err != nil {
				t.Fatal(err)
			}

			type result struct {
				raw string
				err error
			}
			done := make(chan result, 1)
			go func() {
				raw, err := mint(tn, member.Account.ID)
				done <- result{raw, err}
			}()
			var early *result
			for early == nil {
				select {
				case r := <-done:
					early = &r
					continue
				default:
				}
				var waiting int
				if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
WHERE wait_event_type = 'Lock' AND query LIKE '%FROM memberships%'`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting > 0 {
					break
				}
			}
			if early != nil {
				t.Fatalf("the mint finished while the membership drop was uncommitted (err=%v); it read the dropped role", early.err)
			}
			if err := drop.Commit(); err != nil {
				t.Fatal(err)
			}
			r := <-done
			if !errors.Is(r.err, store.ErrNotMember) {
				t.Fatalf("mint after the membership drop committed = %v, want ErrNotMember", r.err)
			}
		})
	}
}
