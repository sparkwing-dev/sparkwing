package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

func persistClaimRequirementsTx(ctx context.Context, tx *storeTx, team Team, selectors []string) (string, error) {
	terms := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		if strings.TrimSpace(selector) == "" {
			continue
		}
		alternatives := strings.Split(selector, ",")
		for i := range alternatives {
			alternatives[i] = strings.TrimSpace(alternatives[i])
		}
		alternatives = slices.DeleteFunc(alternatives, func(label string) bool { return label == "" })
		slices.Sort(alternatives)
		alternatives = slices.Compact(alternatives)
		// An empty alternative list remains an unsatisfiable AND term.
		terms = append(terms, strings.Join(alternatives, ","))
	}
	if len(terms) == 0 {
		return "", nil
	}
	slices.Sort(terms)
	terms = slices.Compact(terms)
	body, err := json.Marshal(terms)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	id := hex.EncodeToString(sum[:])
	created, err := tx.execContext(ctx, `INSERT INTO claim_requirements(team,id) VALUES (?,?) ON CONFLICT(team,id) DO NOTHING`, team, id)
	if err != nil {
		return "", err
	}
	count, err := created.RowsAffected()
	if err != nil || count == 0 {
		return id, err
	}
	for term, selector := range terms {
		for _, label := range strings.Split(selector, ",") {
			if _, err := tx.execContext(ctx, `INSERT INTO claim_requirement_terms(team,requirements,term,label) VALUES (?,?,?,?)`, team, id, term, label); err != nil {
				return "", err
			}
		}
	}
	return id, nil
}

func (q *claimSQL) requirementsMatchSQL(team, requirements string, matches func(string) string) string {
	return `NOT EXISTS (SELECT 1 FROM claim_requirement_terms term WHERE term.team=` + team + ` AND term.requirements=` + requirements + ` GROUP BY term.term HAVING MAX(CASE WHEN term.label<>'' AND ` + matches("term.label") + ` THEN 1 ELSE 0 END)=0)`
}
