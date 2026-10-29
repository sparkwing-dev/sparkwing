package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestChildCeilingTeamSetting(t *testing.T) {
	s, spec := machineEnrollment(t)
	tenant := &Tenant{s: s, team: "a"}
	a := teamAuthorization(t, s, runstate.Actor{Authority: *spec.Authority}, "a", "PUT", "/api/v1/team/settings")
	initial, err := tenant.TeamSettings(t.Context())
	if err != nil || initial.MaxChildrenPerRoot != 50 {
		t.Fatalf("default child ceiling: %+v %v", initial, err)
	}
	childLimitsFixture(t, s, 50)
	check := func(wantError bool) {
		t.Helper()
		err := s.inTx(t.Context(), func(tx *storeTx) error {
			return enforceChildLimitsTx(t.Context(), tx, "a", "root", 2)
		})
		if (err != nil) != wantError {
			t.Fatalf("child intake at 50 descendants: %v", err)
		}
	}
	check(true)
	for _, ceiling := range []int64{1000, 1000, 20} {
		if err := tenant.SetAuthorizedTeamSettings(t.Context(), a, TeamSettingsUpdate{MaxChildrenPerRoot: &ceiling}); err != nil {
			t.Fatal(err)
		}
		check(ceiling == 20)
	}
	events, err := tenant.BusinessEvents(t.Context(), "team.settings_changed", 1000, "")
	if err != nil || len(events) != 2 {
		t.Fatalf("child ceiling audits: %+v %v", events, err)
	}
	for _, e := range events {
		if e.Actor != "owner" || e.Attrs["max_children_per_root"] == nil || e.Attrs["max_children_per_root"] == e.Attrs["previous_max_children_per_root"] {
			t.Fatalf("missing child ceiling audit: %+v", e)
		}
	}
	s.testOnlyExec(t, `INSERT INTO teams(name,created_at,updated_at) VALUES ('b',1,1) ON CONFLICT(name) DO NOTHING`)
	other, err := (&Tenant{s: s, team: "b"}).TeamSettings(t.Context())
	if err != nil || other.MaxChildrenPerRoot != 50 {
		t.Fatalf("changed another team's ceiling: %+v %v", other, err)
	}
	for _, ceiling := range []int64{-1, 0, 1001, 9223372036854775807} {
		var invalid *apierr.Error
		if err := tenant.SetAuthorizedTeamSettings(t.Context(), a, TeamSettingsUpdate{MaxChildrenPerRoot: &ceiling}); !errors.As(err, &invalid) || invalid.Code != apierr.BadRequest {
			t.Fatalf("invalid ceiling %d: %v", ceiling, err)
		}
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_child_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	ceiling := int64(200)
	if err := tenant.SetAuthorizedTeamSettings(t.Context(), a, TeamSettingsUpdate{MaxChildrenPerRoot: &ceiling}); err == nil {
		t.Fatal("changed ceiling without its audit")
	}
	check(true)
}

func TestChildCeilingDatabaseRange(t *testing.T) {
	s, _ := machineEnrollment(t)
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := tx.execContext(t.Context(), `UPDATE teams SET max_children_per_root=1000 WHERE name='a'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, ceiling := range []int64{-1, 0, 1001, 9223372036854775807} {
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			_, err := tx.execContext(t.Context(), `UPDATE teams SET max_children_per_root=? WHERE name='a'`, ceiling)
			return err
		}); err == nil {
			t.Fatalf("database accepted child ceiling %d", ceiling)
		}
	}
	current, err := (&Tenant{s: s, team: "a"}).TeamSettings(t.Context())
	if err != nil || current.MaxChildrenPerRoot != 1000 {
		t.Fatalf("rejected update changed child ceiling: %+v %v", current, err)
	}
}

func TestMachineEnrollmentTeamSettingProperty(t *testing.T) {
	property := func(enabled, personal bool, roleIndex uint8) bool {
		s, spec := machineEnrollment(t)
		role := []Role{RoleOwner, RoleEditor, RoleReader}[roleIndex%3]
		member := authorityMember(t, s, "member", role)
		tenant := &Tenant{s: s, team: "a"}
		owner := runstate.Actor{Authority: *spec.Authority}
		a := teamAuthorization(t, s, owner, "a", "PUT", "/api/v1/team/settings")
		if err := tenant.SetAuthorizedTeamSettings(t.Context(), a, TeamSettingsUpdate{MembersCanAddMachines: &enabled}); err != nil {
			t.Fatal(err)
		}
		spec.Authority, spec.Personal, spec.ReservedPools = &member.Authority, personal, nil
		issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
		allowed := role == RoleOwner || personal && enabled && role != RoleReader
		if !allowed && personal && role != RoleReader && !strings.Contains(fmt.Sprint(err), "members_can_add_machines") {
			return false
		}
		return (err == nil) == allowed && (issued.Raw != "") == allowed
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 48})); err != nil {
		t.Fatal(err)
	}
}

func TestSharedMachineManagementRequiresCurrentAdministration(t *testing.T) {
	for _, personal := range []bool{false, true} {
		t.Run(fmt.Sprint(personal), func(t *testing.T) {
			s, spec := machineEnrollment(t)
			spec.Personal, spec.ReservedPools = personal, nil
			issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			owner := runstate.Actor{Authority: *spec.Authority}
			tenant := &Tenant{s: s, team: "a"}
			a := inventoryAuthorization(t, s, owner, "a", issued.Capability.Prefix)
			rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='editor' WHERE account_id='owner'`)
			rotation, err := tenant.RotateCapability(t.Context(), a, 0, nil)
			if (err == nil) != personal || (rotation.Issued.Raw != "") != personal {
				t.Fatalf("rotation after demotion: %+v %v", rotation, err)
			}
			ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}
			if personal {
				ref.ID = rotation.Issued.Capability.Prefix
			}
			if err := tenant.RevokeAuthority(t.Context(), owner, ref); (err == nil) != personal {
				t.Fatalf("revocation after demotion: %v", err)
			}
		})
	}
}

func TestTeamSettingAuditsOnlyCommittedChanges(t *testing.T) {
	s, spec := machineEnrollment(t)
	now := time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)
	s.clock = func() time.Time { return now }
	tenant := &Tenant{s: s, team: "a"}
	initial, err := tenant.TeamSettings(t.Context())
	if err != nil || !initial.MembersCanAddMachines {
		t.Fatalf("default settings: %+v %v", initial, err)
	}
	owner := runstate.Actor{Authority: *spec.Authority}
	a := teamAuthorization(t, s, owner, "a", "PUT", "/api/v1/team/settings")
	for _, enabled := range []bool{false, false, true} {
		if err := tenant.SetAuthorizedTeamSettings(t.Context(), a, TeamSettingsUpdate{MembersCanAddMachines: &enabled}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := tenant.BusinessEvents(t.Context(), "team.settings_changed", 1000, "")
	if err != nil || len(events) != 2 {
		t.Fatalf("setting audits: %+v %v", events, err)
	}
	for _, event := range events {
		if event.Actor != "owner" || event.Team != "a" || !event.At.Equal(now) || event.Attrs["members_can_add_machines"] == nil || event.Attrs["previous_members_can_add_machines"] == event.Attrs["members_can_add_machines"] {
			t.Fatalf("missing audit facts: %+v", event)
		}
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_setting_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if err := tenant.SetAuthorizedTeamSettings(t.Context(), a, TeamSettingsUpdate{MembersCanAddMachines: new(bool)}); err == nil {
		t.Fatal("committed setting without its audit")
	}
	current, err := tenant.TeamSettings(t.Context())
	if err != nil || !current.MembersCanAddMachines {
		t.Fatalf("failed audit changed setting: %+v %v", current, err)
	}
}

func TestTeamSettingRechecksAuthorityProperty(t *testing.T) {
	property := func(change uint8, suffix uint32) bool {
		s, spec := machineEnrollment(t)
		a := teamAuthorization(t, s, runstate.Actor{Authority: *spec.Authority}, "a", "PUT", "/api/v1/team/settings")
		switch change % 4 {
		case 0:
			rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='editor' WHERE account_id='owner'`)
		case 1:
			rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE account_id='owner'`)
		case 2:
			a.Target.Team = fmt.Sprintf("foreign-%d", suffix)
		case 3:
			a.Principal.Authority.ID = fmt.Sprintf("absent-%d", suffix)
		}
		tenant := &Tenant{s: s, team: "a"}
		ceiling := int64(200)
		err := tenant.SetAuthorizedTeamSettings(t.Context(), a, TeamSettingsUpdate{MembersCanAddMachines: new(bool), MaxChildrenPerRoot: &ceiling})
		current, readErr := tenant.TeamSettings(t.Context())
		events, auditErr := tenant.BusinessEvents(t.Context(), "team.settings_changed", 1000, "")
		return err != nil && readErr == nil && current.MembersCanAddMachines && current.MaxChildrenPerRoot == 50 && auditErr == nil && len(events) == 0
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 24})); err != nil {
		t.Fatal(err)
	}
}

// Turning members_can_add_machines off ends members' personal machines for
// good: turning it back on admits new enrollments but never the old ones.
// Administrators' machines never depended on the setting.
func TestReenablingMemberMachinesDoesNotReviveOldOnes(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	member := authorityMember(t, s, "member", RoleEditor)
	enroll := func(actor runstate.Actor, name string) capability.AuthorityRef {
		t.Helper()
		issued, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &actor.Authority, RegistrationID: "runner:a:" + name, Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: name}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
		if err != nil {
			t.Fatalf("enroll %s: %v", name, err)
		}
		return capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}
	}
	old, admins := enroll(member, "old"), enroll(owner, "admins")
	a := teamAuthorization(t, s, owner, "a", "PUT", "/api/v1/team/settings")
	tenant := &Tenant{s: s, team: "a"}
	for _, enabled := range []bool{false, true} {
		if err := tenant.SetAuthorizedTeamSettings(t.Context(), a, TeamSettingsUpdate{MembersCanAddMachines: &enabled}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AsOperator().ResolveAuthority(t.Context(), old); err == nil {
		t.Fatal("re-enabling members_can_add_machines revived a member's earlier machine")
	}
	if _, err := s.AsOperator().ResolveAuthority(t.Context(), admins); err != nil {
		t.Fatalf("an administrator's machine after the toggle: %v", err)
	}
	if _, err := s.AsOperator().ResolveAuthority(t.Context(), enroll(member, "new")); err != nil {
		t.Fatalf("a member's machine enrolled after re-enabling: %v", err)
	}
}

// high_trust is off by default, changed only by an administrator, and audited.
func TestHighTrustSettingIsAuditedAdministration(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	tenant := &Tenant{s: s, team: "a"}
	if settings, err := tenant.TeamSettings(t.Context()); err != nil || settings.HighTrust {
		t.Fatalf("default high_trust: %+v %v", settings, err)
	}
	on := true
	if err := tenant.SetAuthorizedTeamSettings(t.Context(), teamAuthorization(t, s, owner, "a", "PUT", "/api/v1/team/settings"), TeamSettingsUpdate{HighTrust: &on}); err != nil {
		t.Fatal(err)
	}
	if settings, err := tenant.TeamSettings(t.Context()); err != nil || !settings.HighTrust {
		t.Fatalf("high_trust after enabling: %+v %v", settings, err)
	}
	events, err := tenant.BusinessEvents(t.Context(), "team.settings_changed", 10, "")
	if err != nil || len(events) != 1 || events[0].Attrs["high_trust"] != true || events[0].Attrs["previous_high_trust"] != false {
		t.Fatalf("high_trust audit: %+v %v", events, err)
	}
}
