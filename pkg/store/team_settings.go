package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// TeamSettings reads the team's machine enrollment policy.
func (t *Tenant) TeamSettings(ctx context.Context) (controllerwire.TeamSettings, error) {
	var enabled bool
	err := t.s.queryRow(ctx, `SELECT members_can_add_machines FROM teams WHERE name=?`, t.team).Scan(&enabled)
	return controllerwire.TeamSettings{MembersCanAddMachines: enabled}, err
}

// SetAuthorizedTeamSettings commits a policy change and its audit under current administration.
func (t *Tenant) SetAuthorizedTeamSettings(ctx context.Context, a capability.Authorized, settings controllerwire.TeamSettings) error {
	return t.teamMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		var previous bool
		if err := tx.QueryRowContext(ctx, `SELECT members_can_add_machines FROM teams WHERE name=?`, t.team).Scan(&previous); err != nil {
			return err
		}
		if previous == settings.MembersCanAddMachines {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE teams SET members_can_add_machines=?,updated_at=? WHERE name=?`, boolInt(settings.MembersCanAddMachines), tx.now().UnixNano(), t.team); err != nil {
			return err
		}
		id, err := newIdentityID()
		if err != nil {
			return err
		}
		return RecordBusinessEvent(tx, BusinessEvent{At: tx.now(), Team: t.team, Kind: "team.settings_changed", SubjectID: id, Actor: p.Capability.Subject.ID, Attrs: map[string]any{"members_can_add_machines": settings.MembersCanAddMachines, "previous_members_can_add_machines": previous}})
	})
}
