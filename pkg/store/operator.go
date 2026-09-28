package store

import (
	"context"
	"strings"
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
