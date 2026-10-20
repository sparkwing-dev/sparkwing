package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func blockMembership(t *testing.T, ctx context.Context, st *store.Store, team store.Team, account string) *runBlocker {
	t.Helper()
	b := &runBlocker{t: t, ctx: ctx, db: st.DB(), done: make(chan error, 2)}
	var err error
	if b.tx, err = b.db.BeginTx(ctx, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.tx.Rollback() })
	if _, err := b.tx.ExecContext(ctx, `SELECT account_id FROM memberships WHERE team=$1 AND account_id=$2 FOR UPDATE`, team, account); err != nil {
		t.Fatal(err)
	}
	if err := b.tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&b.pid); err != nil {
		t.Fatal(err)
	}
	return b
}

// Account resolution share-locked a capability and then its subject's
// membership, while demotion locks the membership and then revokes the
// capabilities its account minted, so the two deadlocked.
func TestPostgresAccountResolutionLocksMembershipBeforeAuthorityAgainstDemotion(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	owner := signIn(t, st, "owner", "owner@example.com")
	tn := tenant(t, st, owner.PersonalTeam)
	member := signIn(t, st, "member", "member@example.com")
	if _, err := st.DB().Exec(`INSERT INTO memberships(team,account_id,role,created_at) VALUES ($1,$2,'editor',1)`, tn.Team(), member.Account.ID); err != nil {
		t.Fatal(err)
	}
	ownerActor := membershipActor(t, st, owner.Account.ID, tn.Team())
	session := membershipActor(t, st, member.Account.ID, tn.Team()).Authority
	issued, err := st.ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &session, Team: string(tn.Team()), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: member.Account.ID}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
	if err != nil {
		t.Fatal(err)
	}
	prefix := issued.Capability.Prefix

	b := blockMembership(t, ctx, st, tn.Team(), member.Account.ID)
	b.start(func() error {
		_, err := st.DelegateCapability(ctx, capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, Audience: capability.Controller, Binding: issued.Capability.Binding, Grants: issued.Capability.Grants})
		return err
	})
	b.waitBlocked(1)
	if b.held(`SELECT 1 FROM capabilities WHERE prefix='` + prefix + `' FOR UPDATE`) {
		t.Fatal("account resolution waits for the membership while holding its authority")
	}
	b.start(func() error {
		return tn.UpdateMembership(ctx, ownerActor, store.MembershipUpdate{AccountID: member.Account.ID, Role: rolePointer(store.RoleReader)})
	})
	b.waitBlocked(2)
	b.release(2)
}
