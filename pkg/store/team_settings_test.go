package store

import (
	"fmt"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestMachineEnrollmentTeamSettingProperty(t *testing.T) {
	property := func(enabled, personal bool, roleIndex uint8) bool {
		s, spec := machineEnrollment(t)
		role := []Role{RoleOwner, RoleEditor, RoleReader}[roleIndex%3]
		member := authorityMember(t, s, "member", role)
		tenant := &Tenant{s: s, team: "a"}
		owner := Actor{Authority: *spec.Authority}
		a := teamAuthorization(t, s, owner, "a", "PUT", "/api/v1/team/settings")
		if err := tenant.SetAuthorizedTeamSettings(t.Context(), a, controllerwire.TeamSettings{MembersCanAddMachines: enabled}); err != nil {
			t.Fatal(err)
		}
		spec.Authority, spec.Personal, spec.ReservedPools = &member.Authority, personal, nil
		issued, err := s.ProvisionCapability(t.Context(), spec)
		allowed := role == RoleOwner || personal && enabled && role != RoleReader
		if !allowed && personal && role != RoleReader && !strings.Contains(fmt.Sprint(err), "members_can_add_machines") {
			return false
		}
		return (err == nil) == allowed && (issued.Raw != "") == allowed
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
}

func TestSharedMachineManagementRequiresCurrentAdministration(t *testing.T) {
	for _, personal := range []bool{false, true} {
		t.Run(fmt.Sprint(personal), func(t *testing.T) {
			s, spec := machineEnrollment(t)
			spec.Personal, spec.ReservedPools = personal, nil
			issued, err := s.ProvisionCapability(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			owner := Actor{Authority: *spec.Authority}
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
	owner := Actor{Authority: *spec.Authority}
	a := teamAuthorization(t, s, owner, "a", "PUT", "/api/v1/team/settings")
	for _, enabled := range []bool{false, false, true} {
		if err := tenant.SetAuthorizedTeamSettings(t.Context(), a, controllerwire.TeamSettings{MembersCanAddMachines: enabled}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := tenant.BusinessEvents(t.Context(), "team.settings_changed")
	if err != nil || len(events) != 2 {
		t.Fatalf("setting audits: %+v %v", events, err)
	}
	for _, event := range events {
		if event.Actor != "owner" || event.Team != "a" || !event.At.Equal(now) || event.Attrs["members_can_add_machines"] == nil || event.Attrs["previous_members_can_add_machines"] == event.Attrs["members_can_add_machines"] {
			t.Fatalf("missing audit facts: %+v", event)
		}
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_setting_audit BEFORE INSERT ON business_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if err := tenant.SetAuthorizedTeamSettings(t.Context(), a, controllerwire.TeamSettings{}); err == nil {
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
		a := teamAuthorization(t, s, Actor{Authority: *spec.Authority}, "a", "PUT", "/api/v1/team/settings")
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
		err := tenant.SetAuthorizedTeamSettings(t.Context(), a, controllerwire.TeamSettings{})
		current, readErr := tenant.TeamSettings(t.Context())
		events, auditErr := tenant.BusinessEvents(t.Context(), "team.settings_changed")
		return err != nil && readErr == nil && current.MembersCanAddMachines && auditErr == nil && len(events) == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}
