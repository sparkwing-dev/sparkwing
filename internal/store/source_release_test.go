package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
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
	fundUnitClaim(t, s)
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
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
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
	setMachineOrigin(t, s, "runner", &CapabilityOrigin{Kind: capability.ProvisionMachine, RegistrationID: "runner:a:runner"})
	rewriteSchemaExec(t, s.db, `INSERT INTO git_credentials(id,team,host,kind,secret,confirmed_at,created_at,updated_at) VALUES ('credential','a','github.com','https','encrypted-secret',1,1,1)`)
	s.claimProfiles = claimProfileFake{profile: match.Profile{Location: "remote"}}
	if kind == capability.UnitPlan {
		s.claimProfiles = claimProfileFake{profile: match.Profile{Location: "cloud"}}
	}
	policy := &sourcePolicyFake{}
	s.sourcePolicy = policy
	p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, capability.Authorized{Principal: p, Binding: p.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}, Access: capability.AccessRule{Liveness: capability.LiveSensitive}}, policy
}

func setMachineOrigin(t *testing.T, s *Store, prefix string, origin *CapabilityOrigin) {
	t.Helper()
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		record, err := tx.operator.loadCapabilityRecordTx(t.Context(), tx, prefix)
		if err != nil {
			return err
		}
		record.Origin = origin
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(t.Context(), `UPDATE capabilities SET authority_json=? WHERE prefix=?`, body, prefix)
		return err
	}); err != nil {
		t.Fatal(err)
	}
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
	if executor.Cloud || !executor.Shared || executor.Team != "a" || !reflect.DeepEqual(executor.AuthorizedRepositories, []string{"gh:1"}) || policy.request.Authorization.Principal.Authority != auth.Principal.Authority {
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
	for _, problem := range []string{"team", "revoked", "parent_revoked", "lease", "generation", "holder", "started", "cancelled", "finished", "deadline", "personal", "unregistered", "unconfirmed_ssh", "credential_host", "repository", "enrollment", "policy", "audit", "commit"} {
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
			case "personal":
				setMachineOrigin(t, s, "runner", &CapabilityOrigin{Personal: true, Kind: capability.ProvisionMachine, RegistrationID: "runner:a:runner"})
			case "unregistered":
				setMachineOrigin(t, s, "runner", nil)
			case "unconfirmed_ssh":
				rewriteSchemaExec(t, s.db, `UPDATE git_credentials SET kind='ssh',confirmed_at=NULL`)
			case "credential_host":
				rewriteSchemaExec(t, s.db, `UPDATE git_credentials SET host='other.example'`)
			case "repository":
				accept, err := match.ParseAccept([]string{"github.com/other/*"})
				if err != nil {
					t.Fatal(err)
				}
				s.claimProfiles = claimProfileFake{profile: match.Profile{Accept: accept}}
			case "enrollment":
				s.claimProfiles = nil
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
	row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
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

func TestSourceReleaseCannotSubstituteCloudForAPersonalMachine(t *testing.T) {
	tenant, auth, _ := sourceReleaseFixture(t)
	setMachineOrigin(t, tenant.s, "runner", &CapabilityOrigin{Personal: true, Kind: capability.ProvisionMachine, RegistrationID: "runner:a:runner"})
	auth.Principal.Capability.Billing = &capability.BillingProvenance{ExecutorClass: "cloud"}
	tenant.s.claimProfiles = claimProfileFake{profile: match.Profile{Name: "cloud", Class: match.ClassCloud, Location: "cloud", Labels: []string{"class=cloud", "location=cloud"}}}
	credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
	if err == nil || credential.Secret != "" || audit.ID != "" {
		t.Fatal("mutable executor facts replaced the awarded executor class")
	}
	rewriteSchemaCount(t, tenant.s.db, "git_credential_releases", 0)
}

func TestSourceReleaseNeverReachesAPersonalMachineProperty(t *testing.T) {
	property := func(personal, registered, ssh bool) bool {
		tenant, auth, _ := sourceReleaseFixture(t)
		s := tenant.s
		s.sourcePolicy = nil
		var origin *CapabilityOrigin
		if registered {
			origin = &CapabilityOrigin{Personal: personal, Kind: capability.ProvisionMachine, RegistrationID: "runner:a:runner"}
		}
		setMachineOrigin(t, s, "runner", origin)
		if ssh {
			rewriteSchemaExec(t, s.db, `UPDATE git_credentials SET kind='ssh'`)
		}
		credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
		var released int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM git_credential_releases`).Scan(&released); err != nil {
			t.Fatal(err)
		}
		if registered && !personal {
			return err == nil && credential.ID == "credential" && audit.MachineID == "runner" && released == 1
		}
		var failure *apierr.Error
		return errors.As(err, &failure) && failure.Details["reason"] == apierr.ReasonPersonalMachine && credential.Secret == "" && audit.ID == "" && released == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}
