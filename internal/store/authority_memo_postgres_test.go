package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type narrowingFixture struct {
	target   *storetest.Target
	st       *store.Store
	tn       *store.Tenant
	owner    runstate.Actor
	settings capability.Authorized
	memberID string
	deletion capability.Authorized
	session  capability.AuthorityRef
	machine  capability.AuthorityRef
	delegate capability.AuthorityRef
	token    capability.AuthorityRef
}

func newNarrowingFixture(t *testing.T) narrowingFixture {
	t.Helper()
	ctx := t.Context()
	f := narrowingFixture{target: storetest.NewPostgres(t)}
	f.st = f.target.Open(t)
	owner := signIn(t, f.st, "owner", "owner@example.com")
	f.tn = tenant(t, f.st, owner.PersonalTeam)
	member := signIn(t, f.st, "member", "member@example.com")
	f.memberID = member.Account.ID
	if _, err := f.st.DB().ExecContext(ctx, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ($1,$2,'editor',1)`, f.tn.Team(), f.memberID); err != nil {
		t.Fatal(err)
	}
	ownerRaw := accountSession(t, f.st, owner.Account.ID, f.tn.Team())
	f.owner = runstate.Actor{Kind: runstate.ActorOperator, Authority: sessionRef(ownerRaw)}
	f.settings = routeAuthority(t, f.st, ownerRaw, "PUT", "/api/v1/team/settings", capability.Target{Team: string(f.tn.Team())})
	allow := true
	if err := f.tn.SetAuthorizedTeamSettings(ctx, f.settings, store.TeamSettingsUpdate{MembersCanAddMachines: &allow}); err != nil {
		t.Fatal(err)
	}
	f.session = sessionRef(accountSession(t, f.st, f.memberID, f.tn.Team()))
	machine, err := f.st.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, Personal: true, Authority: &f.session, Team: string(f.tn.Team()),
		RegistrationID: "runner:" + string(f.tn.Team()) + ":mine", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "mine"},
		Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.machine = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: machine.Capability.Prefix}
	delegate, err := f.st.AsOperator().DelegateCapability(ctx, capability.DelegateSpec{Parent: f.machine, Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim}})
	if err != nil {
		t.Fatal(err)
	}
	f.delegate = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: delegate.Capability.Prefix}
	token, err := f.st.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{
		Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &f.session, Team: string(f.tn.Team()),
		Subject: capability.Subject{Kind: capability.SubjectAccount, ID: f.memberID}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.token = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: token.Capability.Prefix}
	f.deletion = storetest.AccountAuthority(t, f.st, f.memberID)
	for _, ref := range []capability.AuthorityRef{f.delegate, f.token} {
		if err := f.st.ResolveLockedAuthorityForTest(ctx, ref); err != nil {
			t.Fatalf("fixture authority %s does not resolve: %v", ref.ID, err)
		}
	}
	return f
}

func accountSession(t *testing.T, st *store.Store, account string, team store.Team) string {
	t.Helper()
	raw, _, _, err := st.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: account}, team, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sessionRef(raw string) capability.AuthorityRef {
	digest := sha256.Sum256([]byte(raw))
	return capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
}

func routeAuthority(t *testing.T, st *store.Store, raw, method, path string, target capability.Target) capability.Authorized {
	t.Helper()
	route, ok := capability.LookupRoute(method, path)
	if !ok {
		t.Fatalf("no route %s %s", method, path)
	}
	auth, err := capability.NewAuthenticator(st.AsOperator()).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialSession, Raw: raw}, route)
	if err != nil {
		t.Fatal(err)
	}
	a, err := capability.Authorize(t.Context(), st.AsOperator(), route, auth, target)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func narrowings() []struct {
	name   string
	ref    func(narrowingFixture) capability.AuthorityRef
	narrow func(context.Context, narrowingFixture) error
} {
	delegate := func(f narrowingFixture) capability.AuthorityRef { return f.delegate }
	return []struct {
		name   string
		ref    func(narrowingFixture) capability.AuthorityRef
		narrow func(context.Context, narrowingFixture) error
	}{
		{"leaf revoke", delegate, func(ctx context.Context, f narrowingFixture) error {
			return f.tn.RevokeAuthority(ctx, f.owner, f.delegate)
		}},
		{"ancestor revoke", delegate, func(ctx context.Context, f narrowingFixture) error {
			return f.tn.RevokeAuthority(ctx, f.owner, f.machine)
		}},
		{"membership removal", delegate, func(ctx context.Context, f narrowingFixture) error {
			return f.tn.UpdateMembership(ctx, f.owner, store.MembershipUpdate{AccountID: f.memberID})
		}},
		{"demotion below the requirement's grant", delegate, func(ctx context.Context, f narrowingFixture) error {
			return f.tn.UpdateMembership(ctx, f.owner, store.MembershipUpdate{AccountID: f.memberID, Role: rolePointer(store.RoleReader)})
		}},
		{"member machines turned off", delegate, func(ctx context.Context, f narrowingFixture) error {
			allow := false
			return f.tn.SetAuthorizedTeamSettings(ctx, f.settings, store.TeamSettingsUpdate{MembersCanAddMachines: &allow})
		}},
		{"session revoke", func(f narrowingFixture) capability.AuthorityRef { return f.token }, func(ctx context.Context, f narrowingFixture) error {
			return f.tn.RevokeAuthority(ctx, f.owner, f.session)
		}},
		{"account deletion", delegate, func(ctx context.Context, f narrowingFixture) error {
			a := f.deletion
			preview, err := f.st.AsOperator().PreviewAccountDeletion(ctx, a)
			if err != nil {
				return err
			}
			var teams []string
			for _, team := range preview.Teams {
				teams = append(teams, string(team))
			}
			_, err = f.st.AsOperator().DeleteOwnAccount(ctx, a, f.memberID, teams)
			return err
		}},
		{"team deletion", delegate, func(ctx context.Context, f narrowingFixture) error {
			_, _, err := f.st.AsOperator().RequestTeamDeletion(ctx, f.tn.Team())
			return err
		}},
	}
}

func requireAuthorityRefused(t *testing.T, err error) {
	t.Helper()
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != hosterr.Unauthenticated {
		t.Fatalf("resolution after the narrowing committed: %v, want an unauthenticated refusal", err)
	}
}

func isRowLock(query string) bool {
	for _, suffix := range []string{" FOR UPDATE", " FOR SHARE", " FOR NO KEY UPDATE"} {
		if strings.HasSuffix(query, suffix) {
			return true
		}
	}
	return false
}

// A narrowing that commits after discovery but before the locks is seen by
// the verification read taken under those locks.
func TestPostgresNarrowingCommittedDuringDiscoveryIsRefused(t *testing.T) {
	for _, tc := range narrowings() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			t.Cleanup(cancel)
			f := newNarrowingFixture(t)
			paused, resume := make(chan struct{}), make(chan struct{})
			var armed atomic.Bool
			reader := store.OpenQueryHookPostgresForTest(t, f.target.DSN(), func(query string) {
				if isRowLock(query) && armed.CompareAndSwap(true, false) {
					close(paused)
					<-resume
				}
			})
			armed.Store(true)
			done := make(chan error, 1)
			go func() { done <- reader.ResolveLockedAuthorityForTest(ctx, tc.ref(f)) }()
			select {
			case <-paused:
			case err := <-done:
				t.Fatalf("resolution finished without taking a row lock: %v", err)
			}
			if err := tc.narrow(ctx, f); err != nil {
				t.Fatalf("narrowing: %v", err)
			}
			close(resume)
			requireAuthorityRefused(t, <-done)
		})
	}
}

// A writer that resolved its authority holds the rows every narrowing must
// lock, so the narrowing waits for the writer to commit.
func TestPostgresNarrowingWaitsForResolvedWriter(t *testing.T) {
	for _, tc := range narrowings() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			t.Cleanup(cancel)
			f := newNarrowingFixture(t)
			pids, release, writer := make(chan int, 1), make(chan struct{}), make(chan error, 1)
			go func() {
				writer <- f.st.HoldResolvedAuthorityForTest(ctx, tc.ref(f), func(pid int) {
					pids <- pid
					<-release
				})
			}()
			var pid int
			select {
			case pid = <-pids:
			case err := <-writer:
				t.Fatalf("writer did not resolve: %v", err)
			}
			b := &runBlocker{t: t, ctx: ctx, db: f.st.DB(), pid: pid, done: make(chan error, 1)}
			b.start(func() error { return tc.narrow(ctx, f) })
			b.waitBlocked(1)
			close(release)
			if err := <-writer; err != nil {
				t.Fatalf("writer: %v", err)
			}
			if err := <-b.done; err != nil {
				t.Fatalf("narrowing after the writer committed: %v", err)
			}
			requireAuthorityRefused(t, f.st.ResolveLockedAuthorityForTest(ctx, tc.ref(f)))
		})
	}
}
