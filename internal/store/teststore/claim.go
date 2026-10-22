package teststore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// Fixtures rewrite immutable declarations, so each unit owns a private index key.
// Content interning and shared placement holds are exercised through store intake tests.
func projectClaimIndexes(ctx context.Context, db *sql.DB, s *store.Store) error {
	rows, err := db.QueryContext(ctx, `SELECT team,run_id,node_id,'node',execution_json,NULL FROM nodes UNION ALL SELECT t.team,t.id,'',t.unit_kind,r.spec_json,r.request_json FROM triggers t JOIN runs r ON r.team=t.team AND r.id=t.id`)
	if err != nil {
		return err
	}
	type unit struct {
		team, run, node, kind string
		terms                 []string
	}
	var units []unit
	for rows.Next() {
		var u unit
		var raw, request []byte
		if err := rows.Scan(&u.team, &u.run, &u.node, &u.kind, &raw, &request); err != nil {
			return errors.Join(err, rows.Close())
		}
		if u.kind == "node" {
			var execution runspec.JobExecutionSettings
			if err := json.Unmarshal(raw, &execution); err != nil {
				return errors.Join(err, rows.Close())
			}
			u.terms = execution.Requires.Value
		} else {
			var sealed runspec.SealedRequest
			if err := json.Unmarshal(request, &sealed); err != nil {
				return errors.Join(err, rows.Close())
			}
			u.terms = sealed.Request.Options.Requires.Value
			var policy struct {
				Resolved struct {
					Settings struct {
						Options *runspec.Options `json:"options"`
					} `json:"settings"`
				} `json:"resolved"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &policy); err != nil {
					return errors.Join(err, rows.Close())
				}
			}
			if policy.Resolved.Settings.Options != nil {
				u.terms = policy.Resolved.Settings.Options.Requires.Value
			}
		}
		units = append(units, u)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, u := range units {
		identity, err := json.Marshal([]string{u.kind, u.run, u.node})
		if err != nil {
			return err
		}
		key, requirements := string(identity), ""
		u.terms = slices.DeleteFunc(u.terms, func(selector string) bool { return strings.TrimSpace(selector) == "" })
		exec := func(query string, args ...any) error {
			_, err := db.ExecContext(ctx, rebind(s, query), args...)
			return err
		}
		if len(u.terms) > 0 {
			requirements = key
			if err := exec(`INSERT INTO claim_requirements(team,id) VALUES (?,?) ON CONFLICT(team,id) DO NOTHING`, u.team, key); err != nil {
				return err
			}
			if err := exec(`DELETE FROM claim_requirement_terms WHERE team=? AND requirements=?`, u.team, key); err != nil {
				return err
			}
			for term, selector := range u.terms {
				for _, alternative := range strings.Split(selector, ",") {
					if err := exec(`INSERT INTO claim_requirement_terms(team,requirements,term,label) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, u.team, key, term, strings.TrimSpace(alternative)); err != nil {
						return err
					}
				}
			}
		}
		if err := exec(`INSERT INTO claim_contexts(team,id,requirements,unit_kind) VALUES (?,?,?,?) ON CONFLICT(team,id) DO UPDATE SET requirements=excluded.requirements`, u.team, key, requirements, u.kind); err != nil {
			return err
		}
		if u.kind == "node" {
			if err := exec(`UPDATE nodes SET claim_requirements=?,claim_context=? WHERE team=? AND run_id=? AND node_id=?`, requirements, key, u.team, u.run, u.node); err != nil {
				return err
			}
		} else {
			if err := exec(`UPDATE triggers SET claim_requirements=?,claim_context=? WHERE team=? AND id=?`, requirements, key, u.team, u.run); err != nil {
				return err
			}
		}
	}
	return nil
}
