package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func inventoryAuthorization(t *testing.T, s *Store, actor runstate.Actor, team, prefix string) capability.Authorized {
	t.Helper()
	p, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	binding := capability.BindNone
	if prefix != "" {
		binding = capability.BindOwnToken
	}
	access := capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.RunsRead}, Binding: binding, Liveness: capability.LiveCredential}
	if team == "" {
		access.Credential, access.Grants, access.Binding = capability.CredentialDeployment, []capability.Grant{capability.OperatorTokens}, capability.BindNone
	}
	return capability.Authorized{Principal: p, Access: access, Binding: p.Capability.Binding, Target: capability.Target{Team: team, TokenPrefix: prefix}}
}

func inventoryRoot(t *testing.T, s *Store, id, team, owner string) capability.Capability {
	t.Helper()
	c := authorityRoot(id, team)
	authorityPersist(t, s, c)
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET created_by=? WHERE prefix=?`, owner, id)
	return c
}

func TestCapabilityInventoryScopesAndMetadata(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "member", RoleEditor)
	tenant := &Tenant{s: s, team: "a"}
	c := inventoryRoot(t, s, "owned", "a", "member")
	inventoryRoot(t, s, "other", "a", "other")
	inventoryRoot(t, s, "foreign", "b", "member")
	at := s.now().Add(-time.Minute)
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET last_used_at=? WHERE prefix=?`, at.UnixNano(), c.Prefix)
	a := inventoryAuthorization(t, s, actor, "a", "")
	rows, err := tenant.ListCapabilities(t.Context(), a, capability.InventoryFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("inventory: %+v %v", rows, err)
	}
	row := rows[0]
	if row.Prefix != c.Prefix || row.CreatedBy != "member" || row.Subject != c.Subject || row.LastUsedAt == nil || !row.LastUsedAt.Equal(at) || row.Metered {
		t.Fatalf("metadata: %+v", row)
	}
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"digest", "binding", "parent", "reserved_pools", "billing", "authority", "registration", "git_credentials"} {
		if strings.Contains(string(body), `"`+private+`"`) {
			t.Fatalf("private metadata %s", body)
		}
	}
	a.Target.TokenPrefix = c.Prefix
	a.Access.Binding = capability.BindOwnToken
	read, err := tenant.ReadCapability(t.Context(), a)
	if err != nil || read.Prefix != row.Prefix {
		t.Fatalf("detail: %+v %v", read, err)
	}
	a.Target.TokenPrefix = "foreign"
	if row, err := tenant.ReadCapability(t.Context(), a); err == nil || row.Prefix != "" {
		t.Fatalf("foreign detail: %+v %v", row, err)
	}
}

func TestCapabilityInventoryOwnershipProperty(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "member", RoleOwner)
	tenant := &Tenant{s: s, team: "a"}
	rng := rand.New(rand.NewPCG(61, 27))
	wantOwn, wantAdmin := []string{}, []string{}
	for i := range 128 {
		c := authorityRoot(fmt.Sprintf("case-%03d", i), []string{"a", "b"}[rng.IntN(2)])
		c.Subject.Kind = []capability.SubjectKind{capability.SubjectAccount, capability.SubjectMachine, capability.SubjectLocal}[rng.IntN(3)]
		c.Subject.ID = []string{"member", "other"}[rng.IntN(2)]
		c.Audience = []capability.Audience{capability.Controller, capability.NodeHost}[rng.IntN(2)]
		if rng.IntN(3) == 0 {
			c.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "repo", Ref: "main", SHA: "sha", WorkflowRunID: "workflow", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main"}}
		}
		if rng.IntN(4) == 0 {
			c.RevokedAt = new(s.now().Add(-time.Minute))
		}
		owner := []string{"member", "other"}[rng.IntN(2)]
		authorityPersist(t, s, c)
		rewriteSchemaExec(t, s.db, `UPDATE capabilities SET created_by=? WHERE prefix=?`, owner, c.Prefix)
		visible := c.Team == "a" && c.Audience == capability.Controller && c.Binding.Kind == capability.BindingNone && (c.Subject.Kind == capability.SubjectAccount || c.Subject.Kind == capability.SubjectMachine) && c.RevokedAt == nil
		if visible {
			wantAdmin = append(wantAdmin, c.Prefix)
			if owner == "member" || c.Subject.Kind == capability.SubjectAccount && c.Subject.ID == "member" {
				wantOwn = append(wantOwn, c.Prefix)
			}
		}
	}
	a := inventoryAuthorization(t, s, actor, "a", "")
	check := func(want []string) {
		t.Helper()
		rows, err := tenant.ListCapabilities(t.Context(), a, capability.InventoryFilter{})
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, row := range rows {
			got = append(got, row.Prefix)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("inventory escaped ownership: got %v want %v", got, want)
		}
	}
	check(wantAdmin)
	rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='editor' WHERE team='a' AND account_id='member'`)
	check(wantOwn)
	rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
	if rows, err := tenant.ListCapabilities(t.Context(), a, capability.InventoryFilter{}); err == nil || rows != nil {
		t.Fatalf("removed member read: %+v %v", rows, err)
	}
}

func TestCapabilityInventoryFiltersAndRefusals(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "member", RoleOwner)
	tenant := &Tenant{s: s, team: "a"}
	inventoryRoot(t, s, "machine", "a", "member")
	c := authorityRoot("revoked", "a")
	c.RevokedAt = new(s.now().Add(-time.Second))
	authorityPersist(t, s, c)
	a := inventoryAuthorization(t, s, actor, "a", "")
	rows, err := tenant.ListCapabilities(t.Context(), a, capability.InventoryFilter{SubjectKind: capability.SubjectMachine, IncludeRevoked: true})
	if err != nil || len(rows) != 2 {
		t.Fatalf("revoked filter: %+v %v", rows, err)
	}
	for _, problem := range []string{"kind", "team", "rule", "grants", "binding", "target", "principal"} {
		t.Run(problem, func(t *testing.T) {
			forged := a
			forged.Access.Grants = slices.Clone(a.Access.Grants)
			filter := capability.InventoryFilter{}
			switch problem {
			case "kind":
				filter.SubjectKind = "unknown"
			case "team":
				forged.Target.Team = "b"
			case "rule":
				forged.Access.Credential = capability.CredentialAny
			case "grants":
				forged.Access.Grants = nil
			case "binding":
				forged.Access.Binding = capability.BindOwnNode
			case "target":
				forged.Target.RunID = "run"
			case "principal":
				forged.Principal.Authority.ID = "absent"
			}
			rows, err := tenant.ListCapabilities(t.Context(), forged, filter)
			if err == nil || rows != nil {
				t.Fatalf("invalid %s returned %+v %v", problem, rows, err)
			}
		})
	}
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET grants=? WHERE prefix='machine'`, []byte(`["team.admin"]`))
	rows, err = tenant.ListCapabilities(t.Context(), a, capability.InventoryFilter{})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.IntegrityFailure || rows != nil {
		t.Fatalf("corrupt inventory returned partial rows: %+v %v", rows, err)
	}
}

func TestCapabilityInventoryDeploymentIsolation(t *testing.T) {
	s := authorityStore(t)
	root := authorityRoot("operator", "")
	root.Subject.Kind = capability.SubjectOperator
	root.Grants = []capability.Grant{capability.OperatorTokens}
	authorityPersist(t, s, root)
	inventoryRoot(t, s, "tenant", "a", "operator")
	service := authorityRoot("service", "")
	service.Subject.Kind = capability.SubjectService
	authorityPersist(t, s, service)
	delegated := service
	delegated.Prefix = "delegated"
	delegated.Digest = [32]byte{1}
	delegated.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	authorityPersist(t, s, delegated)
	a := inventoryAuthorization(t, s, runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}}, "", "")
	operator := s.AsOperator()
	if operator.Team() != "" {
		t.Fatal("operator has a tenant")
	}
	rows, err := operator.ListCapabilities(t.Context(), a, capability.InventoryFilter{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("deployment inventory: %+v %v", rows, err)
	}
	for _, row := range rows {
		a.Target.TokenPrefix = row.Prefix
		if got, err := operator.ReadCapability(t.Context(), a); err != nil || got.Prefix != row.Prefix {
			t.Fatalf("deployment detail: %+v %v", got, err)
		}
	}
	a.Target.TokenPrefix = "delegated"
	if row, err := operator.ReadCapability(t.Context(), a); err == nil || row.Prefix != "" {
		t.Fatalf("delegated deployment detail: %+v %v", row, err)
	}
}

func inventoryMemberRoot(t *testing.T, s *Store, actor runstate.Actor) capability.Issued {
	t.Helper()
	p, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &actor.Authority, Team: p.Capability.Team, Subject: p.Capability.Subject, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite}})
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

func TestCapabilityRotationMemberAndDelegatedAncestry(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(fmt.Sprint(delegated), func(t *testing.T) {
			s := authorityStore(t)
			actor := authorityMember(t, s, "member", RoleEditor)
			root := inventoryMemberRoot(t, s, actor)
			old := root
			if delegated {
				var err error
				old, err = s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix}, Audience: capability.Controller, Binding: root.Capability.Binding, Grants: root.Capability.Grants})
				if err != nil {
					t.Fatal(err)
				}
			}
			tenant := &Tenant{s: s, team: "a"}
			a := inventoryAuthorization(t, s, actor, "a", old.Capability.Prefix)
			rotation, err := tenant.RotateCapability(t.Context(), a, time.Hour, nil)
			if err != nil || rotation.Issued.Raw == "" || rotation.Previous.Prefix != old.Capability.Prefix || rotation.Previous.CreatedBy != "member" {
				t.Fatalf("rotation: %+v %v", rotation, err)
			}
			next := rotation.Issued.Capability
			if next.Subject != old.Capability.Subject || !slices.Equal(next.Grants, old.Capability.Grants) || !reflect.DeepEqual(next.Parent, old.Capability.Parent) {
				t.Fatalf("changed scope or ancestry: %+v -> %+v", old.Capability, next)
			}
			for _, digest := range [][32]byte{old.Capability.Digest, next.Digest} {
				if _, err := s.AsOperator().LookupCapability(t.Context(), digest); err != nil {
					t.Fatal(err)
				}
			}
			again, err := tenant.RotateCapability(t.Context(), a, time.Hour, nil)
			if err == nil || again.Issued.Raw != "" {
				t.Fatal("scheduled predecessor rotated twice")
			}
			if err := tenant.RevokeAuthority(t.Context(), actor, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: old.Capability.Prefix}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AsOperator().LookupCapability(t.Context(), old.Capability.Digest); err == nil {
				t.Fatal("explicit revocation did not end grace")
			}
			if _, err := s.AsOperator().LookupCapability(t.Context(), next.Digest); err != nil {
				t.Fatalf("predecessor revoke ended replacement: %v", err)
			}
		})
	}
}

func TestCapabilityRotationBearerAndRollback(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "member", RoleEditor)
	old := inventoryMemberRoot(t, s, actor)
	actor = runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: old.Capability.Prefix}}
	tenant := &Tenant{s: s, team: "a"}
	a := inventoryAuthorization(t, s, actor, "a", old.Capability.Prefix)
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_rotation BEFORE UPDATE OF revoked_at ON capabilities BEGIN SELECT RAISE(ABORT,'refused'); END`)
	rotation, err := tenant.RotateCapability(t.Context(), a, 0, nil)
	if err == nil || !reflect.DeepEqual(rotation, capability.Rotation{}) {
		t.Fatalf("rollback exposed replacement: %+v %v", rotation, err)
	}
	rewriteSchemaCount(t, s.db, "capabilities", 1)
	if _, err := s.AsOperator().LookupCapability(t.Context(), old.Capability.Digest); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_rotation`)
	rotation, err = tenant.RotateCapability(t.Context(), a, 0, nil)
	if err != nil || rotation.Issued.Raw == "" {
		t.Fatalf("bearer rotation: %+v %v", rotation, err)
	}
	if _, err := s.AsOperator().LookupCapability(t.Context(), old.Capability.Digest); err == nil {
		t.Fatal("zero grace did not revoke")
	}
	if _, err := s.AsOperator().LookupCapability(t.Context(), rotation.Issued.Capability.Digest); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityRotationScopeProperty(t *testing.T) {
	for i := range 48 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := authorityStore(t)
			actor := authorityMember(t, s, "member", RoleEditor)
			old := inventoryMemberRoot(t, s, actor)
			tenant := &Tenant{s: s, team: "a"}
			a := inventoryAuthorization(t, s, actor, "a", old.Capability.Prefix)
			grace := time.Duration(i) * time.Minute
			var expires *time.Time
			switch i % 8 {
			case 0:
				a.Target.Team = "b"
			case 1:
				rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE account_id='member'`)
			case 2:
				a.Access.Grants = nil
			case 3:
				grace = -time.Nanosecond
			case 4:
				grace = 7*24*time.Hour + time.Nanosecond
			case 5:
				expires = new(s.now().Add(-time.Second))
			case 6:
				a.Target.TokenPrefix = "missing"
			case 7:
				a.Principal.Capability.Grants = append(a.Principal.Capability.Grants, capability.OperatorTokens)
			}
			rotation, err := tenant.RotateCapability(t.Context(), a, grace, expires)
			if i%8 != 7 {
				if err == nil || rotation.Issued.Raw != "" {
					t.Fatalf("unauthorized replacement: %+v %v", rotation, err)
				}
				rewriteSchemaCount(t, s.db, "capabilities", 1)
				return
			}
			if err != nil || !slices.Equal(rotation.Issued.Capability.Grants, old.Capability.Grants) {
				t.Fatalf("forged snapshot changed grants: %+v %v", rotation, err)
			}
		})
	}
}

func TestCapabilityRotationRegisteredOrigins(t *testing.T) {
	for _, kind := range []capability.ProvisionKind{capability.ProvisionBootstrap, capability.ProvisionMachine, capability.ProvisionService} {
		t.Run(string(kind), func(t *testing.T) {
			s, spec := provisionOrigin(t, kind)
			old, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			actor := runstate.Actor{}
			if kind == capability.ProvisionBootstrap {
				actor.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: old.Capability.Prefix}
			} else {
				actor.Authority = *spec.Authority
			}
			a := inventoryAuthorization(t, s, actor, spec.Team, old.Capability.Prefix)
			rotation, err := s.rotateCapability(t.Context(), Team(spec.Team), a, time.Hour, nil)
			if err != nil || !sameRotationScope(old.Capability, rotation.Issued.Capability) {
				t.Fatalf("registered rotation: %+v %v", rotation, err)
			}
			var origin *CapabilityOrigin
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				record, err := tx.operator.loadCapabilityRecordTx(t.Context(), tx, rotation.Issued.Capability.Prefix)
				origin = record.Origin
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if origin == nil || origin.Kind != kind || origin.RegistrationID != spec.RegistrationID {
				t.Fatalf("lost origin: %+v", origin)
			}
			if kind == capability.ProvisionMachine {
				a.Target.TokenPrefix = rotation.Issued.Capability.Prefix
				metadata, err := (&Tenant{s: s, team: "a"}).ReadCapability(t.Context(), a)
				if err != nil || metadata.CreatedBy != "owner" {
					t.Fatalf("lost owner: %+v %v", metadata, err)
				}
			}
		})
	}
}

func TestCapabilityRotationDeadlinesAndAncestryProperty(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "member", RoleEditor)
	tenant := &Tenant{s: s, team: "a"}
	now := time.Unix(1700000000, 123456789).UTC()
	s.clock = func() time.Time { return now }
	rng := rand.New(rand.NewPCG(551, 731))
	for i := range 24 {
		start := now
		old := inventoryMemberRoot(t, s, actor)
		child, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: old.Capability.Prefix}, Audience: capability.Controller, Grants: old.Capability.Grants, Binding: old.Capability.Binding})
		if err != nil {
			t.Fatal(err)
		}
		a := inventoryAuthorization(t, s, actor, "a", old.Capability.Prefix)
		grace := time.Duration(rng.Int64N(int64(time.Hour))) + 1
		rotation, err := tenant.RotateCapability(t.Context(), a, grace, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: old.Capability.Prefix}); err != nil {
			t.Fatalf("rotation ended predecessor before grace: %v", err)
		}
		end := start.Add(grace)
		for _, at := range []time.Time{start, end.Add(-time.Nanosecond), end, end.Add(time.Nanosecond)} {
			now = at
			for _, c := range []capability.Capability{old.Capability, child.Capability, rotation.Issued.Capability} {
				_, err := s.AsOperator().LookupCapability(t.Context(), c.Digest)
				want := c.Prefix == rotation.Issued.Capability.Prefix || at.Before(end)
				if (err == nil) != want {
					t.Fatalf("case %d at %s old=%t: %v", i, at, c.Prefix == old.Capability.Prefix, err)
				}
			}
		}
	}
}

func TestCapabilityRotationRefusesWideningAndOriginLoss(t *testing.T) {
	for _, problem := range []string{"parent_expiry", "registration", "missing_origin"} {
		t.Run(problem, func(t *testing.T) {
			s, spec := provisionOrigin(t, capability.ProvisionMachine)
			old, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			actor := runstate.Actor{Authority: *spec.Authority}
			tenant := &Tenant{s: s, team: "a"}
			expires := new(s.now().Add(2 * time.Hour))
			if problem == "parent_expiry" {
				old, err = s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: old.Capability.Prefix}, Audience: capability.Controller, Grants: old.Capability.Grants, Binding: old.Capability.Binding, ExpiresAt: old.Capability.ExpiresAt, Repositories: old.Capability.Repositories})
				if err != nil {
					t.Fatal(err)
				}
			}
			if problem == "origin_policy" {
			}
			if problem == "missing_origin" || problem == "registration" {
				if err := s.inTx(t.Context(), func(tx *storeTx) error {
					record, err := tx.operator.loadCapabilityRecordTx(t.Context(), tx, old.Capability.Prefix)
					if err != nil {
						return err
					}
					if problem == "registration" {
						record.Origin.RegistrationID = "runner:b:machine"
					} else {
						record.Origin = nil
					}
					body, err := json.Marshal(record)
					if err != nil {
						return err
					}
					_, err = tx.execContext(t.Context(), `UPDATE capabilities SET authority_json=? WHERE prefix=?`, body, old.Capability.Prefix)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if problem == "scheduled_revoke" {
				expires = old.Capability.ExpiresAt
			}
			a := inventoryAuthorization(t, s, actor, "a", old.Capability.Prefix)
			var before int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM capabilities`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			rotation, err := tenant.RotateCapability(t.Context(), a, time.Hour, expires)
			if err == nil || rotation.Issued.Raw != "" {
				t.Fatalf("invalid rotation: %+v %v", rotation, err)
			}
			rewriteSchemaCount(t, s.db, "capabilities", before)
			if _, err := s.AsOperator().LookupCapability(t.Context(), old.Capability.Digest); err != nil {
				t.Fatalf("failure ended original: %v", err)
			}
		})
	}
}

func TestCapabilityRotationAdministratorCannotRotateMemberToken(t *testing.T) {
	s := authorityStore(t)
	member := authorityMember(t, s, "member", RoleEditor)
	owner := authorityMember(t, s, "owner", RoleOwner)
	old := inventoryMemberRoot(t, s, member)
	a := inventoryAuthorization(t, s, owner, "a", old.Capability.Prefix)
	tenant := &Tenant{s: s, team: "a"}
	rotation, err := tenant.RotateCapability(t.Context(), a, time.Hour, nil)
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.Forbidden || rotation.Issued.Raw != "" {
		t.Fatalf("admin rotated a member token: %+v %v", rotation, err)
	}
	rewriteSchemaCount(t, s.db, "capabilities", 1)
	if _, err := s.AsOperator().LookupCapability(t.Context(), old.Capability.Digest); err != nil {
		t.Fatalf("refused rotation ended the member token: %v", err)
	}
	if err := tenant.RevokeAuthority(t.Context(), owner, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: old.Capability.Prefix}); err != nil {
		t.Fatalf("admin revoke: %v", err)
	}
}

func TestCapabilityRotationCompetingTransactions(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rotation.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			other, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := other.Close(); err != nil {
					t.Error(err)
				}
			})
			rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
			actor := authorityMember(t, s, "member", RoleEditor)
			old := inventoryMemberRoot(t, s, actor)
			a := inventoryAuthorization(t, s, actor, "a", old.Capability.Prefix)
			tenants := []*Tenant{{s: s, team: "a"}, {s: other, team: "a"}}
			start := make(chan struct{})
			results := make(chan struct {
				rotation capability.Rotation
				err      error
			}, 2)
			for i := range 2 {
				go func() {
					<-start
					var rotation capability.Rotation
					var err error
					if revoke && i == 1 {
						err = tenants[i].RevokeAuthority(t.Context(), actor, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: old.Capability.Prefix})
					} else {
						rotation, err = tenants[i].RotateCapability(t.Context(), a, time.Hour, nil)
					}
					results <- struct {
						rotation capability.Rotation
						err      error
					}{rotation, err}
				}()
			}
			close(start)
			rotations, successes := 0, 0
			for range 2 {
				result := <-results
				if result.err == nil {
					successes++
					if result.rotation.Issued.Raw != "" {
						rotations++
					}
				} else {
					var failure *apierr.Error
					if !errors.As(result.err, &failure) || failure.Code != apierr.LockHeld {
						t.Fatalf("competing transaction: %v", result.err)
					}
				}
			}
			if !revoke && (rotations != 1 || successes != 1) || revoke && (successes < 1 || rotations > 1) {
				t.Fatalf("rotations=%d successes=%d revoke=%t", rotations, successes, revoke)
			}
			rewriteSchemaCount(t, s.db, "capabilities", 1+rotations)
			_, err = s.AsOperator().LookupCapability(t.Context(), old.Capability.Digest)
			if revoke && err == nil {
				t.Fatal("competing rotation undid explicit revocation")
			}
		})
	}
}

func TestCapabilityRotationRetainsMeteredMachine(t *testing.T) {
	s, spec := provisionOrigin(t, capability.ProvisionMachine)
	operator := authorityRoot("operator", "")
	operator.Subject.Kind = capability.SubjectOperator
	operator.Grants = []capability.Grant{capability.OperatorTokens}
	authorityPersist(t, s, operator)
	spec.Authority = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Prefix}
	spec.Metered = true
	old, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	actor := runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "owner"}}
	a := inventoryAuthorization(t, s, actor, "a", old.Capability.Prefix)
	rotation, err := (&Tenant{s: s, team: "a"}).RotateCapability(t.Context(), a, time.Hour, nil)
	if err != nil || !rotation.Issued.Capability.RootMetered || rotation.Previous.CreatedBy != "operator" {
		t.Fatalf("metered replacement: %+v %v", rotation, err)
	}
}

func TestCapabilityRevocationRefusesAlreadyEnded(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "member", RoleEditor)
	old := inventoryMemberRoot(t, s, actor)
	tenant := &Tenant{s: s, team: "a"}
	target := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: old.Capability.Prefix}
	if err := tenant.RevokeAuthority(t.Context(), actor, target); err != nil {
		t.Fatal(err)
	}
	if err := tenant.RevokeAuthority(t.Context(), actor, target); err == nil {
		t.Fatal("effective revocation was accepted again")
	}
}

func TestCapabilityRotationBearerCannotExtendExpiry(t *testing.T) {
	s := authorityStore(t)
	member := authorityMember(t, s, "member", RoleEditor)
	p, err := s.AsOperator().ResolveAuthority(t.Context(), member.Authority)
	if err != nil {
		t.Fatal(err)
	}
	expiry := s.now().Add(time.Hour).Truncate(time.Second)
	old, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &member.Authority, Team: "a", Subject: p.Capability.Subject, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}, ExpiresAt: &expiry})
	if err != nil {
		t.Fatal(err)
	}
	bearer := runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: old.Capability.Prefix}}
	tenant := &Tenant{s: s, team: "a"}
	a := inventoryAuthorization(t, s, bearer, "a", old.Capability.Prefix)
	for _, later := range []time.Time{expiry.Add(time.Second), expiry.Add(30 * 24 * time.Hour)} {
		rotation, err := tenant.RotateCapability(t.Context(), a, time.Hour, &later)
		if err == nil || rotation.Issued.Raw != "" {
			t.Fatalf("bearer extended expiry to %v: %+v", later, rotation.Issued.Capability.ExpiresAt)
		}
	}
	rewriteSchemaCount(t, s.db, "capabilities", 1)
	rotation, err := tenant.RotateCapability(t.Context(), a, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := rotation.Issued.Capability.ExpiresAt
	if next == nil || next.After(expiry) {
		t.Fatalf("replacement expiry %v exceeds %v", next, expiry)
	}
	if !rotation.RevokesAt.After(s.now()) || rotation.RevokesAt.After(expiry) {
		t.Fatalf("grace deadline %v", rotation.RevokesAt)
	}
}

func TestCapabilityRotationSessionMayExtendExpiry(t *testing.T) {
	s := authorityStore(t)
	member := authorityMember(t, s, "member", RoleEditor)
	p, err := s.AsOperator().ResolveAuthority(t.Context(), member.Authority)
	if err != nil {
		t.Fatal(err)
	}
	expiry := s.now().Add(time.Hour).Truncate(time.Second)
	old, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &member.Authority, Team: "a", Subject: p.Capability.Subject, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}, ExpiresAt: &expiry})
	if err != nil {
		t.Fatal(err)
	}
	later := expiry.Add(24 * time.Hour)
	rotation, err := (&Tenant{s: s, team: "a"}).RotateCapability(t.Context(), inventoryAuthorization(t, s, member, "a", old.Capability.Prefix), time.Hour, &later)
	if err != nil || rotation.Issued.Capability.ExpiresAt == nil || !rotation.Issued.Capability.ExpiresAt.Equal(later) {
		t.Fatalf("session rotation: %+v %v", rotation.Issued.Capability.ExpiresAt, err)
	}
}

func adminRotationToken(t *testing.T, s *Store, owner runstate.Actor, expiry time.Time) capability.Issued {
	t.Helper()
	p, err := s.AsOperator().ResolveAuthority(t.Context(), owner.Authority)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &owner.Authority, Team: "a", Subject: p.Capability.Subject, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead, capability.TeamAdmin}, ExpiresAt: &expiry})
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

func TestCapabilityRotationBearersNeverExtendExpiry(t *testing.T) {
	for _, shape := range []string{"child rotates parent", "peer admins"} {
		t.Run(shape, func(t *testing.T) {
			s := authorityStore(t)
			owner := authorityMember(t, s, "owner", RoleOwner)
			hour := s.now().Add(time.Hour).Truncate(time.Second)
			target := adminRotationToken(t, s, owner, hour)
			var caller capability.Issued
			if shape == "child rotates parent" {
				var err error
				caller, err = s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: target.Capability.Prefix}, Audience: capability.Controller, Binding: target.Capability.Binding, Grants: target.Capability.Grants, ExpiresAt: &hour})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				caller = adminRotationToken(t, s, owner, hour.Add(-time.Minute))
			}
			bearer := runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: caller.Capability.Prefix}}
			tenant := &Tenant{s: s, team: "a"}
			a := inventoryAuthorization(t, s, bearer, "a", target.Capability.Prefix)
			later := hour.Add(30 * 24 * time.Hour)
			if rotation, err := tenant.RotateCapability(t.Context(), a, time.Minute, &later); err == nil || rotation.Issued.Raw != "" {
				t.Fatalf("bearer extended expiry: %+v", rotation.Issued.Capability.ExpiresAt)
			}
			rotation, err := tenant.RotateCapability(t.Context(), a, time.Minute, nil)
			if err != nil {
				t.Fatal(err)
			}
			ceiling := hour
			if shape == "peer admins" {
				ceiling = hour.Add(-time.Minute)
			}
			if next := rotation.Issued.Capability.ExpiresAt; next == nil || next.After(ceiling) {
				t.Fatalf("replacement expiry %v exceeds %v", next, ceiling)
			}
		})
	}
}
