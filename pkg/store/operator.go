package store

import (
	"context"
	"strings"
)

// Business event kinds for what an operator does to a team from the
// operator console. Each carries the operator as its actor and the reason
// they gave.
const (
	BusinessEventOperatorCreditGranted = "operator.credit_granted"
	BusinessEventOperatorTeamFrozen    = "operator.team_frozen"
	BusinessEventOperatorTeamUnfrozen  = "operator.team_unfrozen"
)

// TeamMatch is one team a search found, with its owners' email addresses.
type TeamMatch struct {
	Slug        Team
	DisplayName string
	Owners      []string
}

const maxTeamMatches = 20

// SearchTeams finds up to 20 teams whose slug, display name or owner's email
// contains q, ignoring case.
func (s *Store) SearchTeams(ctx context.Context, q string) (_ []TeamMatch, err error) {
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(strings.TrimSpace(q))) + "%"
	rows, err := s.query(ctx, `
		SELECT t.name, t.display_name, COALESCE(a.email, '') FROM teams t
		LEFT JOIN memberships m ON m.team = t.name AND m.role = 'owner'
		LEFT JOIN accounts a ON a.id = m.account_id
		WHERE t.name IN (
			SELECT t2.name FROM teams t2
			LEFT JOIN memberships m2 ON m2.team = t2.name AND m2.role = 'owner'
			LEFT JOIN accounts a2 ON a2.id = m2.account_id
			WHERE LOWER(t2.name) LIKE ? ESCAPE '\' OR LOWER(t2.display_name) LIKE ? ESCAPE '\'
			   OR LOWER(COALESCE(a2.email, '')) LIKE ? ESCAPE '\')
		ORDER BY t.name, a.email`, like, like, like)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []TeamMatch
	for rows.Next() {
		var slug, display, owner string
		if err := rows.Scan(&slug, &display, &owner); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].Slug != Team(slug) {
			if n == maxTeamMatches {
				break
			}
			out = append(out, TeamMatch{Slug: Team(slug), DisplayName: display})
		}
		if owner != "" {
			last := &out[len(out)-1]
			last.Owners = append(last.Owners, owner)
		}
	}
	return out, rows.Err()
}

// RecordOperatorEvent records ev on t in a transaction of its own.
func (t *Tenant) RecordOperatorEvent(ctx context.Context, ev BusinessEvent) (err error) {
	subject, err := newCreditID("op")
	if err != nil {
		return err
	}
	ev.Team, ev.SubjectID = t.team, subject
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := RecordBusinessEvent(tx, ev); err != nil {
		return err
	}
	return tx.Commit()
}
