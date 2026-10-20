package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestPostgresEnrollmentLocksTeamBeforeAuthorizerMembership(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	owner := signIn(t, st, "owner", "owner@example.com")
	tn := tenant(t, st, owner.PersonalTeam)
	authority := membershipActor(t, st, owner.Account.ID, tn.Team()).Authority
	b := &runBlocker{t: t, ctx: ctx, db: st.DB(), done: make(chan error, 1)}
	var err error
	if b.tx, err = st.DB().BeginTx(ctx, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.tx.Rollback() })
	if _, err := b.tx.ExecContext(ctx, `SELECT name FROM teams WHERE name=$1 FOR UPDATE`, tn.Team()); err != nil {
		t.Fatal(err)
	}
	if err := b.tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&b.pid); err != nil {
		t.Fatal(err)
	}
	b.start(func() error {
		issued, err := st.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{
			Kind: capability.ProvisionMachine, Authority: &authority, Team: string(tn.Team()),
			Subject:        capability.Subject{Kind: capability.SubjectMachine, ID: "runner"},
			RegistrationID: "runner:" + string(tn.Team()) + ":runner",
			Binding:        capability.Binding{Kind: capability.BindingNone},
			Grants:         []capability.Grant{capability.NodesClaim}, Repositories: []string{"github.com/acme/*"},
		})
		if err == nil || issued.Raw != "" {
			return errors.New("demoted authorizer enrolled a runner")
		}
		return nil
	})
	b.waitBlocked(1)
	if b.held(`SELECT account_id FROM memberships WHERE team='` + string(tn.Team()) + `' AND account_id='` + owner.Account.ID + `' FOR UPDATE`) {
		t.Fatal("enrollment holds authorizer membership while waiting for the team")
	}
	if _, err := b.tx.ExecContext(ctx, `UPDATE memberships SET role='reader' WHERE team=$1 AND account_id=$2`, tn.Team(), owner.Account.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-b.done; err != nil {
		t.Fatal(err)
	}
	var roots int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM capabilities WHERE team=$1 AND subject_kind='machine'`, tn.Team()).Scan(&roots); err != nil || roots != 0 {
		t.Fatalf("demotion left %d machine roots: %v", roots, err)
	}
}
