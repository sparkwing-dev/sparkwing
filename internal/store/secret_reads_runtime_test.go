package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	modernsqlite "modernc.org/sqlite"
)

func secretReadStoreFixture(t *testing.T) (*Tenant, capability.Authorized) {
	t.Helper()
	tenant, req, now := planFixture(t, runspec.DispatchHost)
	if err := seedSecret(t, tenant, Secret{Name: "KEY", Value: "sealed", Pipeline: "p", Masked: true}, *now); err != nil {
		t.Fatal(err)
	}
	req.Seal.Resolved.SecretNames = []string{"KEY"}
	tenant.s.planSecretOpener = fakePlanSecretOpener(func(_ context.Context, _ Team, row Secret) (string, error) { return row.Value, nil })
	if _, err := tenant.AcceptPlan(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	p, err := tenant.s.AsOperator().ResolveAuthority(t.Context(), req.Actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	c := p.Capability
	c.Grants = append(c.Grants, capability.SecretsRelease)
	c.Grants = append(c.Grants, capability.NodesClaim)
	rewriteSchemaExec(t, tenant.s.db, `DELETE FROM capabilities WHERE prefix=?`, c.Prefix)
	authorityPersist(t, tenant.s, c)
	p, err = tenant.s.AsOperator().ResolveAuthority(t.Context(), req.Actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	return tenant, capability.Authorized{Principal: p, Binding: c.Binding, Target: capability.Target{Team: "a", SecretName: "KEY"}, Access: capability.AccessRule{Credential: capability.CredentialClaim, Grants: []capability.Grant{capability.SecretsRelease}, Binding: capability.BindDeclaredSecret, Liveness: capability.LiveSensitive}}
}

func TestSecretReadClaimScopePrecedenceProperty(t *testing.T) {
	tenant, a := secretReadStoreFixture(t)
	random := rand.New(rand.NewPCG(902, 781))
	for range 48 {
		shared, scoped := random.IntN(2) == 0, random.IntN(2) == 0
		value := fmt.Sprintf("value/%x", random.Uint64())
		rewriteSchemaExec(t, tenant.s.db, `DELETE FROM secrets`)
		for _, row := range []Secret{{Name: "KEY", Value: "team/" + value, Shared: shared, Masked: true}, {Name: "KEY", Pipeline: "other", Value: "foreign-pipeline", Masked: true}} {
			if err := seedSecret(t, tenant, row, tenant.s.now()); err != nil {
				t.Fatal(err)
			}
		}
		if scoped {
			if err := seedSecret(t, tenant, Secret{Name: "KEY", Pipeline: "p", Value: "pipeline/" + value, Masked: true}, tenant.s.now()); err != nil {
				t.Fatal(err)
			}
		}
		row, err := tenant.ReadSecret(t.Context(), a)
		if !shared && !scoped {
			if err == nil || row != nil {
				t.Fatalf("unshared fallback released: %+v, %v", row, err)
			}
			continue
		}
		want := "team/" + value
		if scoped {
			want = "pipeline/" + value
		}
		if err != nil || row == nil || row.Value != want {
			t.Fatalf("shared=%v scoped=%v: %+v, %v", shared, scoped, row, err)
		}
	}
}

func TestSecretReadCommitsBoundReleaseAndNeverDetachesAnImplicitRun(t *testing.T) {
	tenant, a := secretReadStoreFixture(t)
	row, err := tenant.ReadSecret(t.Context(), a)
	if err != nil || row == nil || row.Value != "sealed" || row.Pipeline != "p" {
		t.Fatalf("read: %+v %v", row, err)
	}
	events, err := tenant.BusinessEvents(t.Context(), "secret.released", 1000, "")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind != "secret.released" {
			continue
		}
		count++
		body := event.Attrs
		if body["name_digest"] != digestAudit("KEY").String() || !strings.HasPrefix(event.Actor, string(a.Principal.Capability.Subject.Kind)+":") || body["generation"] != float64(a.Binding.Generation) || event.RunID != a.Binding.RunID {
			t.Fatalf("audit: %+v", event)
		}
	}
	if count != 1 {
		t.Fatalf("release audits: %d", count)
	}
	rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER refuse_release BEFORE INSERT ON audit_events WHEN NEW.kind='secret.released' BEGIN SELECT RAISE(ABORT,'refused'); END`)
	if row, err := tenant.ReadSecret(t.Context(), a); err == nil || row != nil {
		t.Fatalf("unaudited read: %+v %v", row, err)
	}
	rewriteSchemaExec(t, tenant.s.db, `DROP TRIGGER refuse_release`)
	rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER cancel_on_release AFTER INSERT ON audit_events WHEN NEW.kind='secret.released' BEGIN UPDATE runs SET cancel_requested_at=1 WHERE id=NEW.run_id; END`)
	if row, err := tenant.ReadSecret(t.Context(), a); err == nil || row != nil {
		t.Fatalf("read outlived authorization: %+v %v", row, err)
	}
	var cancelled int
	if err := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE cancel_requested_at IS NOT NULL`).Scan(&cancelled); err != nil || cancelled != 0 {
		t.Fatalf("failed release did not roll back: %d %v", cancelled, err)
	}
}

func TestSecretReadAuthorityAndSelectionProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(35, 841))
	for i := range 48 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			tenant, a := secretReadStoreFixture(t)
			s := tenant.s
			switch random.IntN(12) {
			case 0:
				a.Target.Team = "b"
			case 1:
				a.Target.RunID = "other"
			case 2:
				// A run-bound read selects the pipeline its provenance speaks for, whatever the caller names.
				a.Target.Pipeline = "other"
				if row, err := tenant.ReadSecret(t.Context(), a); err != nil || row == nil || row.Pipeline != "p" {
					t.Fatalf("caller pipeline steered a run-bound read: %+v %v", row, err)
				}
				return
			case 3:
				a.Access.Liveness = capability.LiveReporting
			case 4:
				a.Access.Grants = []capability.Grant{capability.SecretsRead}
			case 5:
				rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, a.Principal.Authority.ID)
			case 6:
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET lease_expires_at=?`, s.now().UnixNano())
			case 7:
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET generation=generation+1`)
			case 8:
				rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=1`)
			case 9:
				rewriteSchemaExec(t, s.db, `UPDATE secrets SET only_on='prod'`)
			case 10:
				rewriteSchemaExec(t, s.db, `UPDATE secrets SET pipeline='',shared=0`)
			case 11:
				a.Target.SecretName = "UNDECLARED"
			}
			if row, err := tenant.ReadSecret(t.Context(), a); err == nil || row != nil {
				t.Fatalf("invalid read %d: %+v %v", i, row, err)
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE kind='secret.released'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("refused release audit: %d %v", count, err)
			}
		})
	}
}

func TestSecretReadMemberScopesAndMaskedRefusal(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	a.Target.SecretName = "KEY"
	for _, row := range []Secret{{Name: "KEY", Value: "team"}, {Name: "KEY", Pipeline: "build", Value: "pipeline"}, {Name: "MASKED", Value: "hidden", Masked: true}} {
		if err := tenant.WriteSecret(t.Context(), a, sealedSecretFixture(t, tenant, row)); err != nil {
			t.Fatal(err)
		}
	}
	for _, pipeline := range []string{"", "build", "other"} {
		a.Target.Pipeline = pipeline
		row, err := tenant.ReadSecret(t.Context(), a)
		want := "team"
		if pipeline == "build" {
			want = "pipeline"
		}
		if err != nil || row == nil || openSecretFixture(t, tenant, row) != want {
			t.Fatalf("member scope %q: %+v %v", pipeline, row, err)
		}
	}
	a.Target.Pipeline, a.Target.SecretName = "", "MASKED"
	if row, err := tenant.ReadSecret(t.Context(), a); err == nil || row != nil {
		t.Fatalf("masked member read: %+v %v", row, err)
	}
	a.Target.SecretName, a.Target.RunID = "KEY", "foreign"
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('b','foreign',1,'build','running',?,'request',1,'gh:1','host','execute',?,'account',1,'r30',30)`, []byte(`{"version":1,"provenance":{"trust":"asserted","pipeline":"build","writer":"account/fixture"}}`), []byte(`[]`))
	if row, err := tenant.ReadSecret(t.Context(), a); err == nil || row != nil {
		t.Fatalf("foreign run selector: %+v %v", row, err)
	}
}

func TestSecretReadJobClaimRetainsAccessAfterStart(t *testing.T) {
	tenant, owner := secretReadStoreFixture(t)
	tenant.s.claimProfiles = claimProfileFake{profile: match.Profile{}}
	b := owner.Binding
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: owner.Principal.Authority, CapabilityPrefix: owner.Principal.Capability.Prefix, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(b.RunID), Holder: b.Holder, Generation: runspec.ClaimGeneration(b.Generation)}}}
	ref := runspec.NodeRef{RunID: runspec.RunID(b.RunID), NodeID: "job"}
	reconciled, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: ref, Actor: actor, Policy: runstate.SourceResume})
	if err != nil || reconciled.Claim == nil {
		t.Fatalf("job claim: %+v %v", reconciled, err)
	}
	claim := reconciled.Claim
	p, err := tenant.s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	a := owner
	a.Principal, a.Binding = p, p.Capability.Binding
	if row, err := tenant.ReadSecret(t.Context(), a); err != nil || row == nil {
		t.Fatalf("unstarted job read: %+v %v", row, err)
	}
	holder := runstate.Actor{Kind: runstate.ActorHolder, Authority: p.Authority, CapabilityPrefix: claim.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: ref.RunID, NodeID: ref.NodeID, Holder: claim.Holder, Generation: claim.Generation}}
	admitExecution(t, tenant, holder)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: ref, Actor: holder, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: new(runspec.AttemptOrdinal)}); err != nil {
		t.Fatal(err)
	}
	if row, err := tenant.ReadSecret(t.Context(), a); err != nil || row == nil {
		t.Fatalf("running job read: %+v %v", row, err)
	}
	bound := p.Capability.Binding
	bound.Attempt = 1
	child, err := tenant.s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: p.Authority, Audience: capability.Controller, Binding: bound, Grants: []capability.Grant{capability.SecretsRelease}, ExpiresAt: p.Capability.ExpiresAt, Repositories: p.Capability.Repositories})
	if err != nil {
		t.Fatal(err)
	}
	specific := a
	specific.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Capability.Prefix}
	if row, err := tenant.ReadSecret(t.Context(), specific); err != nil || row == nil {
		t.Fatalf("current attempt read: %+v %v", row, err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET attempt=attempt+1 WHERE run_id=? AND node_id=?`, ref.RunID, ref.NodeID)
	if row, err := tenant.ReadSecret(t.Context(), specific); err == nil || row != nil {
		t.Fatalf("stale attempt read: %+v %v", row, err)
	}
	if row, err := tenant.ReadSecret(t.Context(), a); err != nil || row == nil {
		t.Fatalf("claim-wide read after retry: %+v %v", row, err)
	}
	a.Target.SecretName = "EXTRA_GENERATED_SECRET"
	if row, err := tenant.ReadSecret(t.Context(), a); err == nil || row != nil {
		t.Fatalf("job broadened pipeline declarations: %+v %v", row, err)
	}
}

func TestSecretReadRunnerNameCannotImpersonateAccount(t *testing.T) {
	tenant, a := secretReadStoreFixture(t)
	name := a.Principal.Capability.Subject.ID
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?, 'other@example.test',1,1)`, name)
	if _, err := tenant.ReadSecret(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	var actor string
	var account int
	if err := tenant.s.db.QueryRow(`SELECT actor,actor_is_account FROM audit_events WHERE kind='secret.released'`).Scan(&actor, &account); err != nil || actor == name || actor == "" || account != 0 {
		t.Fatalf("runner named account attributed as account: actor=%q account=%d err=%v", actor, account, err)
	}
}

func TestSecretReadRefusesLeaseExpiredWhileWaitingForLock(t *testing.T) {
	tenant, authorized := secretReadStoreFixture(t)
	s := tenant.s
	var expires int64
	if err := s.db.QueryRow(`SELECT lease_expires_at FROM triggers WHERE id=?`, authorized.Binding.RunID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secret.db")
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		t.Fatal(err)
	}
	raw, err := (&modernsqlite.Driver{}).Open(path)
	if err != nil {
		t.Fatal(err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	var waited atomic.Bool
	recording := &authorizationConnection{Conn: raw, beforeLock: func(query string) {
		if strings.HasPrefix(query, "SELECT id FROM runs ") && waited.CompareAndSwap(false, true) {
			close(locked)
			<-release
		}
	}}
	s.db = sql.OpenDB(authorizationConnector{conn: recording})
	s.db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = s.db.Close() })
	s.dialect = DialectPostgres
	var clock atomic.Int64
	clock.Store(expires - int64(time.Second))
	s.pinnedClock = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	type result struct {
		secret *Secret
		err    error
	}
	done := make(chan result, 1)
	go func() {
		secret, err := tenant.ReadSecret(t.Context(), authorized)
		done <- result{secret, err}
	}()
	select {
	case <-locked:
	case got := <-done:
		t.Fatalf("read finished before waiting on the run lock: %+v", got)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	clock.Store(expires)
	close(release)
	got := <-done
	if got.secret != nil || got.err == nil {
		t.Fatalf("expired claim released secret after lock wait: %+v, %v", got.secret, got.err)
	}
	var releases int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE kind='secret.released'`).Scan(&releases); err != nil || releases != 0 {
		t.Fatalf("expired release audit count = %d, error = %v", releases, err)
	}
}

func TestSecretReadClockFailureCommitsNothing(t *testing.T) {
	tenant, authorized := secretReadStoreFixture(t)
	s := tenant.s
	s.pinnedClock = s.clock
	connection := recordStoreFences(t, s)
	clockErr := errors.New("clock unavailable during secret release")
	audited, failed := false, false
	connection.beforeExec = func(query string) {
		if strings.HasPrefix(strings.TrimSpace(query), "INSERT INTO audit_events ") {
			// safety: fail the final authority check after the audit has been written.
			audited = true
			s.pinnedClock = nil
		}
	}
	connection.beforeQuery = func(query string) error {
		if audited && query == databaseTimeSQL(s.dialect) {
			failed = true
			return clockErr
		}
		return nil
	}
	secret, err := tenant.ReadSecret(t.Context(), authorized)
	if !audited || !failed || secret != nil || !errors.Is(err, clockErr) {
		t.Fatalf("audited=%v failed=%v secret=%+v error=%v", audited, failed, secret, err)
	}
	var releases int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE kind='secret.released'`).Scan(&releases); err != nil || releases != 0 {
		t.Fatalf("committed releases = %d, error = %v", releases, err)
	}
}
