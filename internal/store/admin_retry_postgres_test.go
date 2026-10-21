package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestPostgresAdminTransactionsRetryDeadlockVictims(t *testing.T) {
	for _, name := range []string{"revocation", "operator_revocation", "membership", "team_deletion", "account_deletion", "purge", "retention", "sign_in", "console_approval"} {
		t.Run(name, func(t *testing.T) {
			st := storetest.OpenPostgres(t)
			st.DB().SetMaxOpenConns(8)
			owner := signIn(t, st, "owner", "retry@example.com")
			tn := tenant(t, st, owner.PersonalTeam)
			now := time.Now()
			table, event, lockQuery := "", "", ""
			var lockArgs []any
			var action func(context.Context) error
			switch name {
			case "revocation":
				actor := membershipActor(t, st, owner.Account.ID, tn.Team())
				table, event = "sessions", "UPDATE"
				lockQuery, lockArgs = `SELECT hash FROM sessions WHERE hash=$1 FOR UPDATE`, []any{actor.Authority.ID}
				action = func(ctx context.Context) error { return tn.RevokeAuthority(ctx, actor, actor.Authority) }
			case "operator_revocation":
				_, root, err := teststore.ProvisionCredential(t.Context(), st, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap})
				if err != nil {
					t.Fatal(err)
				}
				actor := store.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, CapabilityPrefix: root.Prefix}
				table, event = "capabilities", "UPDATE"
				lockQuery, lockArgs = `SELECT prefix FROM capabilities WHERE prefix=$1 FOR UPDATE`, []any{root.Prefix}
				action = func(ctx context.Context) error { return st.AsOperator().RevokeAuthority(ctx, actor, actor.Authority) }
			case "membership":
				member := signIn(t, st, "member", "member@example.com")
				if _, err := st.DB().Exec(`INSERT INTO memberships(team,account_id,role,created_at) VALUES ($1,$2,'editor',1)`, tn.Team(), member.Account.ID); err != nil {
					t.Fatal(err)
				}
				actor := membershipActor(t, st, owner.Account.ID, tn.Team())
				table, event = "memberships", "UPDATE"
				lockQuery, lockArgs = `SELECT account_id FROM memberships WHERE team=$1 AND account_id=$2 FOR UPDATE`, []any{tn.Team(), member.Account.ID}
				action = func(ctx context.Context) error {
					return tn.UpdateMembership(ctx, actor, store.MembershipUpdate{AccountID: member.Account.ID, Role: rolePointer(store.RoleReader)})
				}
			case "team_deletion":
				table, event = "memberships", "DELETE"
				lockQuery, lockArgs = `SELECT account_id FROM memberships WHERE team=$1 AND account_id=$2 FOR UPDATE`, []any{tn.Team(), owner.Account.ID}
				action = func(ctx context.Context) error {
					st.SetTestClock(t, func() time.Time { return now })
					result, _, err := st.AsOperator().RequestTeamDeletion(ctx, tn.Team())
					if err == nil && result.State != store.TeamDeletionPending {
						return fmt.Errorf("deletion result = %+v", result)
					}
					return err
				}
			case "account_deletion":
				table, event = "accounts", "DELETE"
				lockQuery, lockArgs = `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, []any{owner.Account.ID}
				action = func(ctx context.Context) error {
					st.SetTestClock(t, func() time.Time { return now })
					result, err := st.DeleteAccount(ctx, owner.Account.ID)
					if err == nil && (result.AccountID != owner.Account.ID || len(result.DeletedTeams) != 1) {
						return fmt.Errorf("account deletion result = %+v", result)
					}
					return err
				}
				st.SetTestClock(t, func() time.Time { return now })
			case "purge":
				if _, _, err := st.AsOperator().RequestTeamDeletion(t.Context(), tn.Team()); err != nil {
					t.Fatal(err)
				}
				if held, err := st.AsOperator().ClaimTeamDeletion(t.Context(), tn.Team(), "retry", time.Hour); err != nil || !held {
					t.Fatalf("deletion lease = %v: %v", held, err)
				}
				table, event = "team_deletions", "UPDATE"
				lockQuery, lockArgs = `SELECT slug FROM team_deletions WHERE slug=$1 FOR UPDATE`, []any{tn.Team()}
				action = func(ctx context.Context) error {
					st.SetTestClock(t, func() time.Time { return now })
					return st.AsOperator().PurgeTeam(ctx, tn.Team(), "retry", time.Hour)
				}
			case "retention":
				if _, err := st.DB().Exec(`INSERT INTO flow_nonces(kind,nonce,expires_at) VALUES ('identity_link','expired',1)`); err != nil {
					t.Fatal(err)
				}
				lease, err := st.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "retry", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				table, event = "flow_nonces", "DELETE"
				lockQuery = `SELECT nonce FROM flow_nonces WHERE kind='identity_link' AND nonce='expired' FOR UPDATE`
				action = func(ctx context.Context) error {
					_, err := st.AsOperator().MaintainRetention(ctx, store.RetentionRequest{Lease: lease, Limit: 10})
					return err
				}
			case "sign_in":
				table, event = "identities", "UPDATE"
				lockQuery, lockArgs = `SELECT account_id FROM identities WHERE account_id=$1 FOR UPDATE`, []any{owner.Account.ID}
				action = func(ctx context.Context) error {
					st.SetTestClock(t, func() time.Time { return now })
					result, err := st.AsOperator().ResolveSignIn(ctx, googleProfile("owner", "changed@example.com"), store.SignUpConditions{})
					if err == nil && (result.NewAccount || result.Account.ID != owner.Account.ID || result.Account.Email != "changed@example.com") {
						return fmt.Errorf("sign-in result = %+v", result)
					}
					return err
				}
				st.SetTestClock(t, func() time.Time { return now })
			case "console_approval":
				actor := membershipActor(t, st, owner.Account.ID, tn.Team())
				console := st.AsConsole(func(id string) bool { return id == owner.Account.ID })
				a := capability.Authorized{Principal: capability.Principal{Class: capability.CredentialConsole, Authority: actor.Authority, Capability: capability.Capability{Subject: capability.Subject{Kind: capability.SubjectOperator, ID: owner.Account.ID}}}}
				if _, err := st.SetSignUpMode(t.Context(), store.SignUpWaitlist, "review", "operator"); err != nil {
					t.Fatal(err)
				}
				waiting := signIn(t, st, "waiting", "waiting@example.com")
				table, event = "accounts", "UPDATE"
				lockQuery, lockArgs = `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, []any{waiting.Account.ID}
				action = func(ctx context.Context) error {
					result, err := console.ConsoleApproveWaitlist(ctx, a, nil, 1, "retry")
					if err != nil {
						return err
					}
					if len(result) != 1 || result[0].ID != waiting.Account.ID {
						return fmt.Errorf("approval result = %+v", result)
					}
					replay, err := console.ConsoleApproveWaitlist(ctx, a, nil, 1, "retry")
					if err == nil && len(replay) != 0 {
						return fmt.Errorf("approval replay = %+v", replay)
					}
					return err
				}
			}
			assertAdminDeadlockRetry(t, st.DB(), table, event, lockQuery, lockArgs, action)
		})
	}
}

func assertAdminDeadlockRetry(t *testing.T, db *sql.DB, table, event, lockQuery string, lockArgs []any, action func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `CREATE TABLE admin_retry_probe (id int PRIMARY KEY); INSERT INTO admin_retry_probe VALUES (1); CREATE SEQUENCE admin_retry_attempts`); err != nil {
		t.Fatal(err)
	}
	body := `CREATE FUNCTION admin_retry_block() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('admin_retry_attempts'); PERFORM id FROM admin_retry_probe WHERE id=1 FOR UPDATE; RETURN `
	if event == "DELETE" {
		body += "OLD"
	} else {
		body += "NEW"
	}
	if _, err := db.ExecContext(ctx, body+`; END; $$; CREATE TRIGGER admin_retry BEFORE `+event+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION admin_retry_block()`); err != nil {
		t.Fatal(err)
	}
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	var super bool
	if err := blocker.QueryRowContext(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname=current_user`).Scan(&super); err != nil {
		t.Fatal(err)
	}
	if super {
		if _, err := blocker.ExecContext(ctx, `SET LOCAL deadlock_timeout='20s'`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := blocker.ExecContext(ctx, `SELECT id FROM admin_retry_probe WHERE id=1 FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- action(ctx) }()
	for blocked := 0; blocked == 0; {
		select {
		case err := <-done:
			t.Fatalf("admin transaction finished before deadlock barrier: %v", err)
		default:
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	if _, err := blocker.ExecContext(ctx, lockQuery, lockArgs...); err != nil {
		t.Fatalf("blocker was the deadlock victim: %v", err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("admin transaction did not retry: %v", err)
	}
	var attempts int
	if err := db.QueryRowContext(ctx, `SELECT last_value FROM admin_retry_attempts`).Scan(&attempts); err != nil || attempts < 2 {
		t.Fatalf("trigger attempts = %d, want at least 2: %v", attempts, err)
	}
}
