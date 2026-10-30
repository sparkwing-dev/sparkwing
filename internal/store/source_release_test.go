package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func sourceReleaseFixture(t *testing.T) (*Tenant, capability.Authorized) {
	t.Helper()
	return sourceReleaseUnitFixture(t, capability.UnitNode)
}

func sourceReleaseUnitFixture(t *testing.T, kind capability.UnitKind) (*Tenant, capability.Authorized) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	fundUnitClaim(t, s)
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET created_by='owner' WHERE prefix='runner'`)
	setMachineOrigin(t, s, "runner", &CapabilityOrigin{Kind: capability.ProvisionMachine, RegistrationID: "runner:a:runner"})
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
	tenant := &Tenant{s: s, team: "a"}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Source.Location.RemoteURL = "https://github.com/acme/repo"
	run.Request.Provenance, err = run.Request.Provenance.FetchedFrom(1, "acme/repo", "", "")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Provenance, err = run.Request.Provenance.WithSourceCredentialVerified()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id='run'`, raw)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO git_credentials(host_wide,id,team,host,kind,secret,confirmed_at,created_at,updated_at) VALUES (1,'credential','a','github.com','https','encrypted-secret',1,1,1)`)
	s.claimProfiles = claimProfileFake{profile: match.Profile{Location: "remote"}}
	if kind == capability.UnitPlan {
		s.claimProfiles = claimProfileFake{profile: match.Profile{Location: "cloud"}}
	}

	p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, capability.Authorized{Principal: p, Binding: p.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}, Access: capability.AccessRule{Liveness: capability.LiveSensitive}}
}

func setMachineOrigin(t *testing.T, s *Store, prefix string, origin *CapabilityOrigin) {
	t.Helper()
	if origin != nil && origin.Personal {
		authorityMember(t, s, "owner", RoleOwner)
		origin.Requirement = &authorityRequirement{Account: "owner", Grant: capability.RunsWrite, MembersCanAddMachines: true}
	}
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
		_, err = tx.execContext(t.Context(), `UPDATE capabilities SET authority_json=? WHERE prefix=?`, body, prefix)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceReleaseSupportsOwnerUnitsAndGrantedCloudPlacement(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan} {
		t.Run(string(kind), func(t *testing.T) {
			tenant, auth := sourceReleaseUnitFixture(t, kind)
			credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
			if err != nil || credential.ID != "credential" || audit.RunID != "run" || audit.NodeID != "" || audit.Generation != 1 {
				t.Fatalf("owner source: credential=%s audit=%+v err=%v", credential.ID, audit, err)
			}
		})
	}
}

func TestSourceReleaseDerivesFactsAndCommitsAudit(t *testing.T) {
	tenant, auth := sourceReleaseFixture(t)
	authorityMember(t, tenant.s, "owner", RoleReader)
	credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if credential.ID != "credential" || credential.Secret != "encrypted-secret" || audit.ID == "" || audit.Team != "a" || audit.RepositoryKey != "gh:1" || audit.NodeID != "job" || audit.Generation != 1 || audit.MachineID != "runner" || audit.OwnerAccountID != "owner" || !audit.ReleasedAt.Equal(tenant.s.now()) {
		t.Fatalf("release: credential=%s audit=%+v", credential.ID, audit)
	}
	stored, err := tenant.SourceReleaseAudits(t.Context(), 10)
	if err != nil || len(stored) != 1 {
		t.Fatalf("audit did not commit: %+v %v", stored, err)
	}
	assertSourceReleaseAuditProjection(t, stored[0], audit)
}

func TestSourceReleaseRefusesChangedAuthorityBeforeReleasing(t *testing.T) {
	for _, problem := range []string{"team", "revoked", "parent_revoked", "lease", "generation", "holder", "started", "cancelled", "finished", "deadline", "personal", "unregistered", "unconfirmed_ssh", "credential_host", "repository", "audit", "commit"} {
		t.Run(problem, func(t *testing.T) {
			tenant, auth := sourceReleaseFixture(t)
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
				setSourceHost(t, tenant, auth, &capability.HostFacts{Kind: capability.HostPersonal})
			case "unregistered":
				setSourceHost(t, tenant, auth, nil)
			case "unconfirmed_ssh":
				rewriteSchemaExec(t, s.db, `UPDATE git_credentials SET kind='ssh',confirmed_at=NULL`)
			case "credential_host":
				rewriteSchemaExec(t, s.db, `UPDATE git_credentials SET host='other.example'`)
			case "repository":
				run, err := tenant.ReadRun(t.Context(), "run")
				if err != nil {
					t.Fatal(err)
				}
				run.Request.Request.Git.Repository.Key = "gh:999"
				setRunRequest(t, tenant, run.Request)
			case "audit":
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_source_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit refused'); END`)
			case "commit":
				rewriteSchemaExec(t, s.db, `CREATE TABLE source_commit_guard(team TEXT REFERENCES teams(name) DEFERRABLE INITIALLY DEFERRED)`)
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_source_commit AFTER INSERT ON audit_events BEGIN INSERT INTO source_commit_guard(team) VALUES ('missing-team'); END`)
			}
			credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
			if err == nil || credential.Secret != "" || audit.ID != "" {
				t.Fatalf("failed %s release returned data: credential=%s audit=%s error=%v", problem, credential.ID, audit.ID, err)
			}
			rewriteSchemaCount(t, s.db, "audit_events WHERE kind='git_credential.released'", 0)
		})
	}
}

func TestSourceReleaseCannotWidenPersistedFacts(t *testing.T) {
	tenant, auth := sourceReleaseFixture(t)
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

func TestSourceReleaseRefusesExpiredLease(t *testing.T) {
	tenant, auth := sourceReleaseFixture(t)
	row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil {
		t.Fatal(err)
	}
	tenant.s.clock = func() time.Time { return *row.LeaseExpiresAt }
	credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
	if err == nil || credential.Secret != "" || audit.ID != "" {
		t.Fatal("release outlived the locked lease")
	}
	rewriteSchemaCount(t, tenant.s.db, "audit_events WHERE kind='git_credential.released'", 0)
}

func TestSourceReleaseCannotSubstituteCloudForAPersonalMachine(t *testing.T) {
	tenant, auth := sourceReleaseFixture(t)
	setSourceHost(t, tenant, auth, &capability.HostFacts{Kind: capability.HostPersonal})
	auth.Principal.Capability.Billing = &capability.BillingProvenance{ExecutorClass: "cloud"}
	tenant.s.claimProfiles = claimProfileFake{profile: match.Profile{Name: "cloud", Class: match.ClassCloud, Location: "cloud", Labels: []string{"class=cloud", "location=cloud"}}}
	credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
	if err == nil || credential.Secret != "" || audit.ID != "" {
		t.Fatal("mutable executor facts replaced the awarded executor class")
	}
	rewriteSchemaCount(t, tenant.s.db, "audit_events WHERE kind='git_credential.released'", 0)
}

func TestSourceReleaseNeverReachesAPersonalMachineProperty(t *testing.T) {
	property := func(personal, registered, ssh bool) bool {
		tenant, auth := sourceReleaseFixture(t)
		s := tenant.s
		var host *capability.HostFacts
		if registered {
			kind := capability.HostSelfHosted
			if personal {
				kind = capability.HostPersonal
			}
			host = &capability.HostFacts{Kind: kind, MachineID: "runner"}
		}
		setSourceHost(t, tenant, auth, host)
		if ssh {
			rewriteSchemaExec(t, s.db, `UPDATE git_credentials SET kind='ssh'`)
		}
		credential, audit, err := tenant.ReleaseSourceCredential(t.Context(), auth)
		var released int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE kind='git_credential.released'`).Scan(&released); err != nil {
			t.Fatal(err)
		}
		if registered && !personal {
			return err == nil && credential.ID == "credential" && audit.MachineID == "runner" && released == 1
		}
		return err != nil && credential.Secret == "" && audit.ID == "" && released == 0
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 32})); err != nil {
		t.Fatal(err)
	}
}

func TestSourceReleaseRequiresSealedRepositoryAndIntakeCompare(t *testing.T) {
	tenant, auth := sourceReleaseFixture(t)
	authorityMember(t, tenant.s, "owner", RoleReader)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO git_credentials(host_wide,repository_key,id,team,host,kind,secret,confirmed_at,created_at,updated_at) VALUES (0,'gh:1','scoped','a','github.com','https','scoped-secret',1,1,1)`)
	release := func() (GitCredential, error) {
		credential, _, err := tenant.ReleaseSourceCredential(t.Context(), auth)
		return credential, err
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	proven := run.Request
	if proven.Provenance.ProvenRepository() != proven.Request.Git.Repository.Key {
		t.Fatalf("fixture proves %q, want its own repository %q", proven.Provenance.ProvenRepository(), proven.Request.Git.Repository.Key)
	}
	setRunRequest(t, tenant, proven)
	if credential, err := release(); err != nil || credential.ID != "scoped" {
		t.Fatalf("a run proving its repository did not get that repository's credential: %q %v", credential.ID, err)
	}
	for name, proof := range map[string]func() (runspec.Provenance, error){
		"asserted remote commit": func() (runspec.Provenance, error) {
			return runspec.AssertedProvenance(runspec.PrincipalWriter("account", "owner"), "main", fixtureFeatureSHA, "ci")
		},
		"proven without compare": func() (runspec.Provenance, error) {
			p, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "owner"), "main", fixtureFeatureSHA, "ci")
			if err != nil {
				return p, err
			}
			return p.FetchedFrom(1, "acme/repo", "", "")
		},
	} {
		t.Run(name, func(t *testing.T) {
			unproven := proven
			unproven.Provenance, err = proof()
			if err != nil {
				t.Fatal(err)
			}
			setRunRequest(t, tenant, unproven)
			defer setRunRequest(t, tenant, proven)
			var refused *apierr.Error
			if credential, err := release(); !errors.Is(err, ErrNotFound) || !errors.As(err, &refused) || !strings.Contains(refused.Message, "has not proven") {
				t.Fatalf("unproven code got %q %v", credential.ID, err)
			}
		})
	}
}

func setSourceHost(t *testing.T, tenant *Tenant, auth capability.Authorized, host *capability.HostFacts) {
	t.Helper()
	if err := tenant.s.inTx(t.Context(), func(tx *storeTx) error {
		record, err := tx.operator.loadCapabilityRecordTx(t.Context(), tx, auth.Principal.Authority.ID)
		if err != nil {
			return err
		}
		record.Value.Host = host
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		kind := capability.HostKind("")
		if host != nil {
			kind = host.Kind
		}
		_, err = tx.execContext(t.Context(), `UPDATE capabilities SET authority_json=?,host_kind=? WHERE prefix=?`, body, kind, auth.Principal.Authority.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func setRunRequest(t *testing.T, tenant *Tenant, sealed runspec.SealedRequest) {
	t.Helper()
	raw, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET request_json=? WHERE id='run'`, raw)
}
