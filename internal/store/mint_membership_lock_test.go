package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestCapabilityMintWaitsOnAnUncommittedMembershipDrop(t *testing.T) {
	for _, name := range []string{"member", "machine"} {
		t.Run(name, func(t *testing.T) {
			st := storetest.OpenPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			owner := signIn(t, st, "owner-"+name, "owner-"+name+"@example.com")
			member := signIn(t, st, "member-"+name, "member-"+name+"@example.com")
			tn := tenant(t, st, owner.PersonalTeam)
			inv, err := tn.InviteForTest(t, ctx, owner.Account.ID, member.Account.Email, store.RoleOwner, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.AsOperator().AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, member.Account.ID), inv.ID); err != nil {
				t.Fatal(err)
			}

			raw, _, _, err := st.AsOperator().CreateAccountSession(ctx, member.Account, owner.PersonalTeam, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(raw))
			ref := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
			spec := capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &ref, Team: string(owner.PersonalTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: member.Account.ID}, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone}}
			if name == "machine" {
				spec.Kind = capability.ProvisionMachine
				spec.Subject = capability.Subject{Kind: capability.SubjectMachine, ID: "machine"}
				spec.Grants = []capability.Grant{capability.NodesClaim}
				spec.RegistrationID = "runner:" + spec.Team + ":machine"
				spec.Repositories = []string{"github.com/acme/*"}
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
				issued, err := st.AsOperator().ProvisionCapability(ctx, spec)
				done <- result{issued.Raw, err}
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
			if r.err == nil || r.raw != "" {
				t.Fatalf("mint after membership removal returned a bearer: error=%v", r.err)
			}
		})
	}
}
