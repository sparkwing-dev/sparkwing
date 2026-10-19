package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func authorityStore(t *testing.T) *Store {
	t.Helper()
	s := &Store{db: rewriteSchemaDB(t), dialect: DialectSQLite}
	rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)")
	rewriteSchemaExec(t, s.db, "INSERT INTO team_counters(team) VALUES ('a'),('b')")
	return s
}

func authorityRoot(id, team string) capability.Capability {
	return capability.Capability{
		Digest: sha256.Sum256([]byte(id)), Prefix: id, Team: team,
		Subject: capability.Subject{Kind: capability.SubjectMachine, ID: id}, Audience: capability.Controller,
		Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone},
		Issuance: capability.Delegate, IssuedAt: time.Unix(1, 0).UTC(), ReservedPools: []string{}, Repositories: nil,
	}
}

func authorityPersist(t *testing.T, s *Store, c capability.Capability) {
	t.Helper()
	tx, err := s.beginTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(tx)
	if err := persistCapabilityTx(t.Context(), tx, c); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityEnvelopeRefusesProjectionDrift(t *testing.T) {
	s := authorityStore(t)
	authorityPersist(t, s, authorityRoot("root", "a"))
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}
	if _, err := s.AuthGenerations(t.Context(), ref, "a"); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE capabilities SET grants = ? WHERE prefix = 'root'", []byte(`["team.admin"]`))
	if _, err := s.AuthGenerations(t.Context(), ref, "a"); err == nil {
		t.Fatal("accepted index authority wider than envelope")
	}
}

func TestAuthorityGenerationIncludesCrossTeamAncestor(t *testing.T) {
	s := authorityStore(t)
	root := authorityRoot("launcher", "")
	root.Subject.Kind = capability.SubjectService
	root.Grants = []capability.Grant{capability.ClaimsLaunch, capability.OperatorTokens}
	authorityPersist(t, s, root)
	child := authorityRoot("child", "a")
	child.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	authorityPersist(t, s, child)
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix}
	got, err := s.AuthGenerations(t.Context(), ref, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Authorities) != 2 || got.Authorities[1].Authority.ID != root.Prefix {
		t.Fatalf("ancestry = %#v", got)
	}
	if _, err := s.AuthGenerations(t.Context(), ref, "b"); err == nil {
		t.Fatal("accepted foreign membership fence")
	}
	actor := Actor{Authority: *child.Parent, CapabilityPrefix: root.Prefix}
	if err := s.AsOperator().RevokeAuthority(t.Context(), actor, *child.Parent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthGenerations(t.Context(), ref, "a"); err == nil {
		t.Fatal("revoked ancestor stayed live")
	}
	var generation int64
	if err := s.db.QueryRow("SELECT revocation_generation FROM capabilities WHERE prefix = 'launcher'").Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != 1 {
		t.Fatalf("generation = %d", generation)
	}
}

func authorityMember(t *testing.T, s *Store, id string, role Role) Actor {
	t.Helper()
	rewriteSchemaExec(t, s.db, "INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?, ?, 1, 1)", id, id+"@example.test")
	rewriteSchemaExec(t, s.db, "INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a',?,?,1)", id, role)
	rewriteSchemaExec(t, s.db, "INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES (?,?,?,1,?,'a',?)", id, id, "runs.read,runs.write,runs.control,team.admin", time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano(), id)
	return Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: id}}
}

func TestAuthorityMembershipChangesInvalidateSessionDescendants(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	member := authorityMember(t, s, "member", RoleEditor)
	child := authorityRoot("child", "a")
	child.Parent = &member.Authority
	authorityPersist(t, s, child)
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix}
	before, err := s.AuthGenerations(t.Context(), ref, "a")
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	role := RoleReader
	if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member", Role: &role}); err != nil {
		t.Fatal(err)
	}
	after, err := s.AuthGenerations(t.Context(), ref, "a")
	if err != nil {
		t.Fatal(err)
	}
	if after.MembershipGeneration != before.MembershipGeneration+1 {
		t.Fatalf("membership fence did not advance: %#v -> %#v", before, after)
	}
	if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "owner", Role: &role}); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("last owner demotion: %v", err)
	}
	if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthGenerations(t.Context(), ref, "a"); err == nil {
		t.Fatal("removed member descendant stayed live")
	}
}

func TestAuthorityMissingCycleExpiryAndForgedFenceFailClosed(t *testing.T) {
	for _, problem := range []string{"missing", "cycle", "expiry"} {
		t.Run(problem, func(t *testing.T) {
			s := authorityStore(t)
			c := authorityRoot("root", "a")
			if problem == "missing" {
				c.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "gone"}
			}
			if problem == "cycle" {
				c.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}
			}
			if problem == "expiry" {
				expiry := time.Unix(2, 0).UTC()
				c.ExpiresAt = &expiry
			}
			authorityPersist(t, s, c)
			if _, err := s.AuthGenerations(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}, "a"); err == nil {
				t.Fatal("accepted invalid authority")
			}
		})
	}
	s := authorityStore(t)
	actor := authorityMember(t, s, "owner", RoleOwner)
	actor.CapabilityPrefix = "forged"
	err := (&Tenant{s: s, team: "a"}).RevokeAuthority(context.Background(), actor, actor.Authority)
	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Code != apierr.Forbidden {
		t.Fatalf("forged fence: %v", err)
	}
}

func authorityClaimFixture(t *testing.T, s *Store, root capability.Capability) capability.AwardSpec {
	t.Helper()
	authorityPersist(t, s, root)
	future := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('a','run',1,'ci','running',?,'sealed',1,'gh:1','host','execute',?,'account',1,'r30',30)`, []byte(`{"version":1}`), []byte(`[]`))
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES ('a','run','trigger',1,1,?,?,?)`, future, []byte(`[]`), []byte(`[]`))
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,claimed_by,generation,attempt,lease_expires_at,spec_json,execution_json) VALUES ('a','run','job','running',?,7,2,?,?,?)`, root.Subject.ID, future, []byte(`{}`), []byte(`{}`))
	unit, err := encodeAwardUnit(UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
	if err != nil {
		t.Fatal(err)
	}
	return capability.AwardSpec{Claimant: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Unit: capability.UnitNode, UnitID: unit, Purpose: capability.PurposeWork, Delivery: capability.DeliveryDirect}
}

func TestAuthorityAwardDerivesBindingAndBilling(t *testing.T) {
	s := authorityStore(t)
	root := authorityRoot("machine", "a")
	root.ReservedPools = []string{"prod"}
	spec := authorityClaimFixture(t, s, root)
	tx, err := s.beginTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(tx)
	issued, err := s.Issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Award, Award: &spec})
	if err != nil {
		t.Fatal(err)
	}
	c := issued.Capability
	if c.Team != "a" || c.Binding.RunID != "run" || c.Binding.NodeID != "job" || c.Binding.Generation != 7 || c.Binding.Attempt != 0 || c.Binding.Holder != "machine" || len(c.ReservedPools) != 1 || c.Billing == nil || c.Billing.Unit != capability.BillingJob {
		t.Fatalf("award = %#v", c)
	}
	if c.Digest != sha256.Sum256([]byte(issued.Raw)) {
		t.Fatal("bearer digest differs")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 1)
	if _, err := s.AuthGenerations(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityBootstrapExchangeIsAtomicAndSingleUse(t *testing.T) {
	s, bootstrap := adapterBootstrap(t)
	tx, err := s.beginTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(tx)
	exchanged, err := s.Issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Exchange, Exchange: &capability.ExchangeSpec{BootstrapPrefix: bootstrap.Capability.Prefix}})
	if err != nil {
		t.Fatal(err)
	}
	if exchanged.Capability.Parent.ID != "launcher" || exchanged.Capability.Billing.ReservationID != bootstrap.Capability.Billing.ReservationID {
		t.Fatal("exchange detached ancestry or billing")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = s.beginTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(tx)
	if _, err := s.Issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Exchange, Exchange: &capability.ExchangeSpec{BootstrapPrefix: bootstrap.Capability.Prefix}}); err == nil {
		t.Fatal("bootstrap exchanged twice")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 1)
	if _, err := s.AuthGenerations(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: exchanged.Capability.Prefix}, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityAwardRefusesForeignStaleAndUnreservedMeteredClaims(t *testing.T) {
	for _, kind := range []string{"foreign", "stale", "metered"} {
		t.Run(kind, func(t *testing.T) {
			s := authorityStore(t)
			root := authorityRoot("machine", "a")
			if kind == "foreign" {
				root.Team = "b"
			}
			if kind == "metered" {
				root.RootMetered = true
			}
			spec := authorityClaimFixture(t, s, root)
			if kind == "stale" {
				rewriteSchemaExec(t, s.db, "UPDATE nodes SET generation = 0 WHERE run_id = 'run'")
			}
			tx, err := s.beginTx(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackOrLog(tx)
			if _, err := s.Issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Award, Award: &spec}); err == nil {
				t.Fatal("invalid award accepted")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaCount(t, s.db, "capabilities", 1)
			rewriteSchemaCount(t, s.db, "billing_awards", 0)
		})
	}
}

func TestRemoteCacheReadsNeedTheRoleThatRunsCode(t *testing.T) {
	for role, allowed := range map[Role]bool{RoleReader: false, RoleEditor: true, RoleOwner: true} {
		t.Run(string(role), func(t *testing.T) {
			s := authorityStore(t)
			member := authorityMember(t, s, "member", role)
			child := authorityRoot("remote-cache", "a")
			child.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "member"}
			child.Parent = &member.Authority
			child.Grants = []capability.Grant{capability.DataRead}
			child.Repositories = []string{"gh:1"}
			child.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:1", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
			authorityPersist(t, s, child)
			p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix})
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(p.Capability.Grants, capability.DataRead) != allowed {
				t.Fatalf("%s remote cache read grant = %v", role, p.Capability.Grants)
			}
		})
	}
}
