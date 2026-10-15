package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type sourcePolicyFake struct {
	request capability.SourceReleaseRequest
	err     error
	before  func()
}

func (f *sourcePolicyFake) AuthorizeSourceRelease(request capability.SourceReleaseRequest, now time.Time) (capability.SourceReleaseAudit, error) {
	f.request = request
	if f.before != nil {
		f.before()
	}
	if f.err != nil {
		return capability.SourceReleaseAudit{}, f.err
	}
	c := request.Authorization.Principal.Capability
	return capability.SourceReleaseAudit{
		Team: c.Team, CredentialID: request.CredentialID, Host: request.Host,
		RepositoryKey: request.RepositoryKey, RunID: c.Binding.RunID, NodeID: c.Binding.NodeID,
		Generation: c.Binding.Generation, MachineID: request.Executor.MachineID,
		OwnerAccountID: request.Executor.OwnerAccountID, CapabilityPrefix: c.Prefix, ReleasedAt: now,
	}, nil
}

func sourceReleaseFixture(t *testing.T) (*Tenant, capability.Authorized, *sourcePolicyFake) {
	t.Helper()
	return sourceReleaseUnitFixture(t, capability.UnitNode)
}

func sourceReleaseUnitFixture(t *testing.T, kind capability.UnitKind) (*Tenant, capability.Authorized, *sourcePolicyFake) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	demand.Target = &UnitRef{Kind: kind, RunID: "run"}
	if kind == capability.UnitNode {
		demand.Target.NodeID = "job"
	}
	if kind == capability.UnitPlan {
		launcher := authorityRoot("launcher", "")
		launcher.Subject.Kind = capability.SubjectService
		launcher.Grants = []capability.Grant{capability.ClaimsLaunch}
		authorityPersist(t, s, launcher)
		demand.CapabilityPrefix = launcher.Prefix
		rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
		rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
	}
	claim, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Source.Location.RemoteURL = "https://github.com/acme/repo"
	raw, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id='run'`, raw)
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET created_by='owner' WHERE prefix='runner'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO git_credential_machines(team,capability_prefix,enabled_by,enabled_at) VALUES ('a','runner','owner',1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO git_credentials(id,team,host,kind,secret,confirmed_at,created_at,updated_at) VALUES ('credential','a','github.com','https','encrypted-secret',1,1,1)`)
	s.SetClaimProfileResolver(claimProfileFake{profile: match.Profile{Location: "remote"}})
	if kind == capability.UnitPlan {
		s.SetClaimProfileResolver(claimProfileFake{profile: match.Profile{Location: "cloud"}})
		rewriteSchemaExec(t, s.db, `DELETE FROM git_credential_machines`)
	}
	policy := &sourcePolicyFake{}
	s.sourcePolicy = policy
	p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, capability.Authorized{Principal: p, Binding: p.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}, Access: capability.AccessRule{Liveness: capability.LiveSensitive}}, policy
}

func TestSourceReleaseSupportsOwnerUnitsAndGrantedCloudPlacement(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan} {
		t.Run(string(kind), func(t *testing.T) {
			tenant, auth, policy := sourceReleaseUnitFixture(t, kind)
			credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
			if err != nil || credential.ID != "credential" || audit.RunID != "run" || audit.NodeID != "" || audit.Generation != 1 || policy.request.Executor.Cloud != (kind == capability.UnitPlan) {
				t.Fatalf("owner source: credential=%s audit=%+v err=%v", credential.ID, audit, err)
			}
		})
	}
}

func TestSourceReleaseDerivesFactsAndCommitsAudit(t *testing.T) {
	tenant, auth, policy := sourceReleaseFixture(t)
	credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if credential.ID != "credential" || credential.Secret != "encrypted-secret" || audit.ID == "" || audit.Team != "a" || audit.RepositoryKey != "gh:1" || audit.NodeID != "job" || audit.Generation != 1 || audit.MachineID != "runner" || audit.OwnerAccountID != "owner" || !audit.ReleasedAt.Equal(tenant.s.now()) {
		t.Fatalf("release: credential=%s audit=%+v", credential.ID, audit)
	}
	executor := policy.request.Executor
	if executor.Cloud || !executor.OwnerOptedIn || executor.Team != "a" || !reflect.DeepEqual(executor.AuthorizedRepositories, []string{"gh:1"}) || policy.request.Authorization.Principal.Authority != auth.Principal.Authority {
		t.Fatalf("derived request: %+v", policy.request)
	}
	var stored capability.SourceReleaseAudit
	var released int64
	err = tenant.s.db.QueryRow(`SELECT id,team,credential_id,host,repository_key,run_id,node_id,generation,machine_id,owner_account_id,capability_prefix,released_at FROM git_credential_releases`).Scan(&stored.ID, &stored.Team, &stored.CredentialID, &stored.Host, &stored.RepositoryKey, &stored.RunID, &stored.NodeID, &stored.Generation, &stored.MachineID, &stored.OwnerAccountID, &stored.CapabilityPrefix, &released)
	stored.ReleasedAt = time.Unix(0, released).UTC()
	if err != nil || !reflect.DeepEqual(stored, audit) {
		t.Fatalf("audit did not commit: %+v %v", stored, err)
	}
}

func TestSourceReleaseRefusesChangedAuthorityBeforeReleasing(t *testing.T) {
	for _, problem := range []string{"team", "revoked", "parent_revoked", "lease", "generation", "holder", "started", "cancelled", "finished", "deadline", "consent", "wrong_owner", "unconfirmed_ssh", "credential_host", "repository", "enrollment", "policy", "audit", "commit"} {
		t.Run(problem, func(t *testing.T) {
			tenant, auth, policy := sourceReleaseFixture(t)
			s := tenant.s
			switch problem {
			case "team":
				tenant.team = "b"
			case "revoked":
				rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, auth.Principal.Authority.ID)
			case "parent_revoked":
				rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='runner'`)
			case "lease":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=?`, s.now().UnixNano())
			case "generation":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=generation+1`)
			case "holder":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET claimed_by='replacement'`)
			case "started":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET started_at=1`)
			case "cancelled":
				rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=1`)
			case "finished":
				rewriteSchemaExec(t, s.db, `UPDATE runs SET status='success',finished_at=1`)
			case "deadline":
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET expires_at=?`, s.now().UnixNano())
			case "consent":
				rewriteSchemaExec(t, s.db, `DELETE FROM git_credential_machines`)
			case "wrong_owner":
				rewriteSchemaExec(t, s.db, `UPDATE git_credential_machines SET enabled_by='admin'`)
			case "unconfirmed_ssh":
				rewriteSchemaExec(t, s.db, `UPDATE git_credentials SET kind='ssh',confirmed_at=NULL`)
			case "credential_host":
				rewriteSchemaExec(t, s.db, `UPDATE git_credentials SET host='other.example'`)
			case "repository":
				accept, err := match.ParseAccept([]string{"github.com/other/*"})
				if err != nil {
					t.Fatal(err)
				}
				s.SetClaimProfileResolver(claimProfileFake{profile: match.Profile{Accept: accept}})
			case "enrollment":
				s.SetClaimProfileResolver(nil)
			case "policy":
				policy.err = errors.New("policy refused")
			case "audit":
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_source_audit BEFORE INSERT ON git_credential_releases BEGIN SELECT RAISE(ABORT,'audit refused'); END`)
			case "commit":
				rewriteSchemaExec(t, s.db, `CREATE TABLE source_commit_guard(team TEXT REFERENCES teams(name) DEFERRABLE INITIALLY DEFERRED)`)
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_source_commit AFTER INSERT ON git_credential_releases BEGIN INSERT INTO source_commit_guard(team) VALUES ('missing-team'); END`)
			}
			credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
			if err == nil || credential.Secret != "" || audit.ID != "" {
				t.Fatalf("failed %s release returned data: credential=%s audit=%s error=%v", problem, credential.ID, audit.ID, err)
			}
			rewriteSchemaCount(t, s.db, "git_credential_releases", 0)
		})
	}
}

func TestSourceReleaseCannotWidenPersistedFacts(t *testing.T) {
	tenant, auth, _ := sourceReleaseFixture(t)
	property := func(value string, foreign bool) bool {
		forged := auth
		forged.Principal.Capability.Team = "forged-" + value
		forged.Principal.Capability.Grants = nil
		forged.Principal.Capability.Binding = capability.Binding{RunID: value, Generation: 100}
		forged.Binding = forged.Principal.Capability.Binding
		forged.Target = capability.Target{Team: "a", RunID: "run"}
		if foreign {
			forged.Target.RunID = "foreign-" + value
		}
		credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), forged)
		if foreign {
			return err != nil && credential.Secret == "" && audit.ID == ""
		}
		return err == nil && credential.ID == "credential" && audit.RunID == "run" && audit.Team == "a" && audit.Generation == 1
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceReleaseRechecksLeaseAfterPolicyWork(t *testing.T) {
	tenant, auth, policy := sourceReleaseFixture(t)
	row, err := tenant.ReadNode(t.Context(), NodeRef{"run", "job"})
	if err != nil {
		t.Fatal(err)
	}
	policy.before = func() {
		tenant.s.clock = func() time.Time { return *row.LeaseExpiresAt }
	}
	credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
	if err == nil || credential.Secret != "" || audit.ID != "" {
		t.Fatal("release outlived the locked lease")
	}
	rewriteSchemaCount(t, tenant.s.db, "git_credential_releases", 0)
}

func TestSourceReleaseCannotSubstituteCloudForMachineConsent(t *testing.T) {
	tenant, auth, _ := sourceReleaseFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `DELETE FROM git_credential_machines`)
	auth.Principal.Capability.Billing = &capability.BillingProvenance{ExecutorClass: "cloud"}
	tenant.s.SetClaimProfileResolver(claimProfileFake{profile: match.Profile{Name: "cloud", Class: match.ClassCloud, Location: "cloud", Labels: []string{"class=cloud", "location=cloud"}}})
	credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
	if err == nil || credential.Secret != "" || audit.ID != "" {
		t.Fatal("mutable executor facts replaced the awarded executor class")
	}
	rewriteSchemaCount(t, tenant.s.db, "git_credential_releases", 0)
}

func TestSourceConsentRequiresCurrentMachineOwner(t *testing.T) {
	tenant, _, _ := sourceReleaseFixture(t)
	owner := authorityMember(t, tenant.s, "owner", RoleEditor)
	admin := authorityMember(t, tenant.s, "admin", RoleOwner)
	for _, enabled := range []bool{false, true} {
		if err := tenant.SetSourceMachineConsent(t.Context(), admin.Authority, "runner", enabled); err == nil {
			t.Fatal("team administrator consented for another machine owner")
		}
		if err := tenant.SetSourceMachineConsent(t.Context(), owner.Authority, "runner", enabled); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM git_credential_machines WHERE team='a' AND capability_prefix='runner'`).Scan(&n); err != nil || (n == 1) != enabled {
			t.Fatalf("consent=%d enabled=%v err=%v", n, enabled, err)
		}
	}
}

func TestSourceConsentCannotChangeAnotherOwnersMachine(t *testing.T) {
	tenant, _, _ := sourceReleaseFixture(t)
	owner := authorityMember(t, tenant.s, "owner", RoleEditor)
	property := func(value string, mine, enabled bool) bool {
		account := "foreign-" + value
		if mine {
			account = "owner"
		}
		rewriteSchemaExec(t, tenant.s.db, `UPDATE capabilities SET created_by=? WHERE prefix='runner'`, account)
		var before, after int
		if err := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM git_credential_machines`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		err := tenant.SetSourceMachineConsent(t.Context(), owner.Authority, "runner", enabled)
		if readErr := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM git_credential_machines`).Scan(&after); readErr != nil {
			t.Fatal(readErr)
		}
		if !mine {
			return err != nil && after == before
		}
		return err == nil && (after == 1) == enabled
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}
