package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func secretReadStoreFixture(t *testing.T) (*Tenant, capability.Authorized) {
	t.Helper()
	tenant, req, now := planFixture(t, runspec.DispatchHost)
	if err := seedSecret(t, tenant, Secret{Name: "KEY", Value: "sealed", Pipeline: "p", Masked: true}, *now); err != nil {
		t.Fatal(err)
	}
	req.Seal.Resolved.SecretNames = []string{"KEY"}
	tenant.s.SetPlanSecretOpener(fakePlanSecretOpener(func(_ context.Context, _ Team, row Secret) (string, error) { return row.Value, nil }))
	if _, err := tenant.AcceptPlan(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	p, err := tenant.s.ResolveAuthority(t.Context(), req.Actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	c := p.Capability
	c.Grants = append(c.Grants, capability.SecretsRelease)
	c.Grants = append(c.Grants, capability.NodesClaim)
	rewriteSchemaExec(t, tenant.s.db, `DELETE FROM capabilities WHERE prefix=?`, c.Prefix)
	authorityPersist(t, tenant.s, c)
	p, err = tenant.s.ResolveAuthority(t.Context(), req.Actor.Authority)
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
	events, err := tenant.ReadStateEvents(t.Context(), a.Binding.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind != "secret_released" {
			continue
		}
		count++
		var body map[string]any
		if err := json.Unmarshal(event.Payload, &body); err != nil || body["name"] != "KEY" || body["generation"] != float64(a.Binding.Generation) || event.RunID != a.Binding.RunID {
			t.Fatalf("audit: %+v %v", event, err)
		}
	}
	if count != 1 {
		t.Fatalf("release audits: %d", count)
	}
	rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER refuse_release BEFORE INSERT ON events WHEN NEW.kind='secret_released' BEGIN SELECT RAISE(ABORT,'refused'); END`)
	if row, err := tenant.ReadSecret(t.Context(), a); err == nil || row != nil {
		t.Fatalf("unaudited read: %+v %v", row, err)
	}
	rewriteSchemaExec(t, tenant.s.db, `DROP TRIGGER refuse_release`)
	rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER cancel_on_release AFTER INSERT ON events WHEN NEW.kind='secret_released' BEGIN UPDATE runs SET cancel_requested_at=1 WHERE id=NEW.run_id; END`)
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
				a.Target.Pipeline = "other"
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
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind='secret_released'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("refused release audit: %d %v", count, err)
			}
		})
	}
}

func TestSecretReadMemberScopesAndMaskedRefusal(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	a.Target.SecretName = "KEY"
	for _, row := range []Secret{{Name: "KEY", Value: "team"}, {Name: "KEY", Pipeline: "build", Value: "pipeline"}, {Name: "MASKED", Value: "hidden", Masked: true}} {
		if err := tenant.WriteSecret(t.Context(), a, row); err != nil {
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
		if err != nil || row == nil || row.Value != want {
			t.Fatalf("member scope %q: %+v %v", pipeline, row, err)
		}
	}
	a.Target.Pipeline, a.Target.SecretName = "", "MASKED"
	if row, err := tenant.ReadSecret(t.Context(), a); err == nil || row != nil {
		t.Fatalf("masked member read: %+v %v", row, err)
	}
	a.Target.SecretName, a.Target.RunID = "KEY", "foreign"
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('b','foreign',1,'build','running',?,'request',1,'gh:1','host','execute',?,'account',1,'r30',30)`, []byte(`{"version":1}`), []byte(`[]`))
	if row, err := tenant.ReadSecret(t.Context(), a); err == nil || row != nil {
		t.Fatalf("foreign run selector: %+v %v", row, err)
	}
}

func TestSecretReadJobClaimRetainsAccessAfterStart(t *testing.T) {
	tenant, owner := secretReadStoreFixture(t)
	tenant.s.SetClaimProfileResolver(claimProfileFake{profile: match.Profile{Capacity: match.Resources{Cores: 8}}, available: match.Resources{Cores: 8}})
	b := owner.Binding
	actor := Actor{Kind: ActorRunHost, Authority: owner.Principal.Authority, CapabilityPrefix: owner.Principal.Capability.Prefix, Owner: OwnerLeaseRef{Kind: OwnerTrigger, RunID: b.RunID, Holder: b.Holder, Generation: b.Generation}}
	ref := NodeRef{RunID: b.RunID, NodeID: "job"}
	reconciled, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: ref, Actor: actor, Policy: SourceResume})
	if err != nil || reconciled.Claim == nil {
		t.Fatalf("job claim: %+v %v", reconciled, err)
	}
	claim := reconciled.Claim
	p, err := tenant.s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	a := owner
	a.Principal, a.Binding = p, p.Capability.Binding
	if row, err := tenant.ReadSecret(t.Context(), a); err != nil || row == nil {
		t.Fatalf("unstarted job read: %+v %v", row, err)
	}
	holder := Actor{Kind: ActorHolder, Authority: p.Authority, CapabilityPrefix: claim.CapabilityPrefix, Holder: claim.Holder, Generation: claim.Generation}
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: ref, Actor: holder, To: NodeRunning, Cause: CauseStart, SourceAttempt: new(int64)}); err != nil {
		t.Fatal(err)
	}
	if row, err := tenant.ReadSecret(t.Context(), a); err != nil || row == nil {
		t.Fatalf("running job read: %+v %v", row, err)
	}
	bound := p.Capability.Binding
	bound.Attempt = 1
	child, err := tenant.s.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: p.Authority, Audience: capability.Controller, Binding: bound, Grants: []capability.Grant{capability.SecretsRelease}, ExpiresAt: p.Capability.ExpiresAt, Repositories: p.Capability.Repositories})
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
