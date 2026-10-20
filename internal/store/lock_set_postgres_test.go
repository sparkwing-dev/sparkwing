package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestPostgresRotationWaitsForDemotionWithoutHoldingAuthority(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	owner := signIn(t, st, "owner", "owner@example.com")
	tn := tenant(t, st, owner.PersonalTeam)
	otherOwner := signIn(t, st, "other-owner", "other-owner@example.com")
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ($1,$2,'owner',1)`, tn.Team(), otherOwner.Account.ID); err != nil {
		t.Fatal(err)
	}
	actor := membershipActor(t, st, owner.Account.ID, tn.Team())
	issued, err := st.ProvisionCapability(ctx, capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, Authority: &actor.Authority, Team: string(tn.Team()),
		Subject:        capability.Subject{Kind: capability.SubjectMachine, ID: "runner"},
		RegistrationID: "runner:" + string(tn.Team()) + ":runner",
		Binding:        capability.Binding{Kind: capability.BindingNone},
		Grants:         []capability.Grant{capability.NodesClaim}, Repositories: []string{"github.com/acme/*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.ResolveAuthority(ctx, actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	a := capability.Authorized{Principal: p, Target: capability.Target{Team: string(tn.Team()), TokenPrefix: issued.Capability.Prefix}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.BindOwnToken, Liveness: capability.LiveCredential}}
	b := &runBlocker{t: t, ctx: ctx, db: st.DB(), done: make(chan error, 1)}
	b.tx, err = st.DB().BeginTx(ctx, nil)
	if err != nil {
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
		rotation, err := tn.RotateCapability(ctx, a, time.Hour, nil)
		if err == nil || rotation.Issued.Raw != "" {
			return errors.New("demoted administrator rotated shared runner")
		}
		return nil
	})
	b.waitBlocked(1)
	if b.held(`SELECT account_id FROM memberships WHERE team='` + string(tn.Team()) + `' AND account_id='` + owner.Account.ID + `' FOR UPDATE`) {
		t.Fatal("rotation holds authorizer membership while waiting for the team")
	}
	if b.held(`SELECT prefix FROM capabilities WHERE prefix='` + issued.Capability.Prefix + `' FOR UPDATE`) {
		t.Fatal("rotation holds its target authority while waiting for the team")
	}
	if b.held(`SELECT hash FROM sessions WHERE hash='` + actor.Authority.ID + `' FOR UPDATE`) {
		t.Fatal("rotation holds its authorizing session while waiting for the team")
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
	var count int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM capabilities WHERE team=$1`, tn.Team()).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed rotation left %d credentials: %v", count, err)
	}
	var revoked bool
	if err := st.DB().QueryRowContext(ctx, `SELECT revoked_at IS NOT NULL FROM capabilities WHERE prefix=$1`, issued.Capability.Prefix).Scan(&revoked); err != nil || revoked {
		t.Fatalf("failed rotation changed original revocation: %v, %v", revoked, err)
	}
}

func TestPostgresReclaimLocksBothTeamsBeforeInstallationAgainstUnbind(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	previous, next := teamHandle(t, st, "a"), teamHandle(t, st, "z")
	oldAuth, nextAuth := githubAppAuthorization(t, st, previous), githubAppAuthorization(t, st, next)
	p, err := st.ResolveAuthority(ctx, nextAuth.Principal.Authority)
	if err != nil {
		t.Fatal(err)
	}
	nextAuth.Principal = p
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('github','501',$1,'admin@example.test',1,1)`, p.Capability.Subject.ID); err != nil {
		t.Fatal(err)
	}
	row := acmeInstallation()
	if _, err := previous.TestOnlySeedGitHubInstallation(ctx, row, time.Now()); err != nil {
		t.Fatal(err)
	}
	row.ConnectedBy, row.Reclaim = "", true
	b := &runBlocker{t: t, ctx: ctx, db: st.DB(), done: make(chan error, 2)}
	b.tx, err = st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.tx.Rollback() })
	if _, err := b.tx.ExecContext(ctx, `SELECT name FROM teams WHERE name='a' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if err := b.tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&b.pid); err != nil {
		t.Fatal(err)
	}
	b.start(func() error {
		_, err := next.BindAuthorizedGitHubInstallation(ctx, nextAuth, row)
		return err
	})
	b.waitBlocked(1)
	for _, query := range []string{
		`SELECT name FROM teams WHERE name='z' FOR UPDATE`,
		`SELECT installation_id FROM github_app_installations WHERE installation_id=7 FOR UPDATE`,
		`SELECT hash FROM sessions WHERE hash='` + nextAuth.Principal.Authority.ID + `' FOR UPDATE`,
	} {
		if b.held(query) {
			t.Fatalf("reclaim holds a later lock while waiting for the previous team: %s", query)
		}
	}
	b.start(func() error {
		err := previous.UnbindAuthorizedGitHubInstallation(ctx, oldAuth, 7)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	})
	b.waitBlocked(2)
	b.release(2)
	got, err := st.AsOperator().GitHubAppInstallationTeam(ctx, 7)
	if err != nil || got.Team != "z" {
		t.Fatalf("reclaim/unbind result: %+v, %v", got, err)
	}
}
