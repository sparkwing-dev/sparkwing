package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// Each route is an indexed requirement set, not a remembered scan position.
// Its head is re-evaluated by the same predicate used for named claims.
type claimHead struct {
	Team         Team
	Requirements string
	Ordinal      int64
	Arrived      int64
	Unit         UnitRef
}

func compareClaimHeads(a, b claimHead) int {
	for _, cmp := range []int{
		compareInt64(a.Ordinal, b.Ordinal), compareInt64(a.Arrived, b.Arrived),
		strings.Compare(string(a.Team), string(b.Team)), strings.Compare(string(a.Unit.RunID), string(b.Unit.RunID)),
		strings.Compare(string(a.Unit.Kind), string(b.Unit.Kind)), strings.Compare(string(a.Unit.NodeID), string(b.Unit.NodeID)),
		compareInt64(a.Unit.SourceGeneration, b.Unit.SourceGeneration),
	} {
		if cmp != 0 {
			return cmp
		}
	}
	return 0
}

func compareInt64(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (q *claimSQL) sourceScope(alias string, kind capability.UnitKind) string {
	var conditions []string
	if q.freshOnly {
		arrived := alias + `.ready_at`
		if alias == "n" {
			// SQLite needs the explicit predicate to use the partial readiness index.
			conditions = append(conditions, `n.status<>'done'`)
		}
		if alias == "t" {
			arrived = alias + `.available_at`
		}
		if alias == "e" {
			arrived = `n.finished_at`
		}
		conditions = append(conditions, arrived+`>clock.now-`+fmt.Sprint(int64(limits.RankingWindow)))
	}
	if q.team != "" {
		conditions = append(conditions, alias+`.team=`+q.param(q.team))
		if q.freshOnly && alias == "e" {
			conditions = append(conditions, `n.team=`+q.param(q.team))
		}
	}
	if q.route {
		conditions = append(conditions, alias+`.team=route.team`)
		if kind == capability.UnitEval {
			conditions = append(conditions, `route.requirements=''`, `n.team=route.team`)
		} else {
			conditions = append(conditions, alias+`.claim_requirements=route.requirements`)
		}
	}
	if q.target != nil {
		conditions = append(conditions, alias+`.team=(SELECT named.team FROM runs named WHERE named.id=`+q.param(string(q.target.RunID))+`)`)
		column := alias + `.run_id`
		if alias == "t" {
			column = alias + `.id`
		}
		conditions = append(conditions, column+`=`+q.param(string(q.target.RunID)))
		if alias == "n" {
			conditions = append(conditions, alias+`.node_id=`+q.param(string(q.target.NodeID)))
		}
		if alias == "e" {
			conditions = append(conditions, alias+`.source_node_id=`+q.param(string(q.target.NodeID)), alias+`.source_generation=`+q.param(q.target.SourceGeneration))
		}
	}
	if q.after != nil {
		a := q.after
		arrived, run, node, generation := alias+`.ready_at`, alias+`.run_id`, alias+`.node_id`, `0`
		if alias == "t" {
			arrived, run, node = alias+`.available_at`, alias+`.id`, `''`
		}
		if alias == "e" {
			arrived, node, generation = `n.finished_at`, alias+`.source_node_id`, alias+`.source_generation`
		}
		conditions = append(conditions, `(`+arrived+`,`+run+`,`+q.param(string(kind))+`,`+node+`,`+generation+`)>(`+q.param(a.Arrived)+`,`+q.param(string(a.Unit.RunID))+`,`+q.param(string(a.Unit.Kind))+`,`+q.param(string(a.Unit.NodeID))+`,`+q.param(a.Unit.SourceGeneration)+`)`)
	}
	if len(conditions) == 0 {
		return `1=1`
	}
	return strings.Join(conditions, ` AND `)
}

func (q *claimSQL) headSQL(kinds []capability.UnitKind) string {
	function, suffix := `json_array`, ``
	if q.dialect == DialectPostgres {
		function, suffix = `jsonb_build_array`, `::text`
	}
	result := function + `(team,run_id,node_id,source_generation,kind,arrived)` + suffix
	var heads []string
	for _, kind := range kinds {
		source := q.source([]capability.UnitKind{kind})
		order := `arrived,run_id`
		if kind == capability.UnitNode || kind == capability.UnitEval {
			order += `,node_id`
		}
		if kind == capability.UnitEval {
			order += `,source_generation`
		}
		if len(kinds) == 1 {
			return `(SELECT ` + result + ` FROM (` + source + `) head ORDER BY ` + order + ` LIMIT 1)`
		}
		// Constant identity fields must not turn simultaneous arrivals into a sort.
		heads = append(heads, `SELECT * FROM (SELECT * FROM (`+source+`) indexed_head ORDER BY `+order+` LIMIT 1) kind_head`)
	}
	return `(SELECT ` + result + ` FROM (` + strings.Join(heads, ` UNION ALL `) + `) head ORDER BY arrived,team,run_id,kind,node_id,source_generation LIMIT 1)`
}

func (q *claimSQL) requirementRoutes(current claimFacts) (string, error) {
	labels, err := q.jsonParam(current.Labels)
	if err != nil {
		return "", err
	}
	teamScope := func() string {
		clause := `1=1`
		if q.team != "" {
			clause = `tm.name=` + q.param(q.team)
		} else if current.Cloud == 0 {
			clause = `tm.name=` + q.param(current.Team)
		}
		if len(current.Excluded) > 0 {
			raw, _ := json.Marshal(current.Excluded)
			clause += ` AND tm.name NOT IN (SELECT ` + q.text("excluded.value") + ` FROM ` + q.array(q.param(string(raw)), "excluded") + `)`
		}
		return clause
	}
	routes := `SELECT tm.name AS team,'' AS requirements,tm.claim_award_ordinal AS ordinal FROM teams tm WHERE tm.name IN (` + liveTeamsSQL + `) AND ` + teamScope() + ` UNION ALL SELECT tm.name,matched.requirements,tm.claim_award_ordinal FROM matched JOIN teams tm ON tm.name=matched.team WHERE tm.name IN (` + liveTeamsSQL + `) AND ` + teamScope()
	// Every term must have an alternative in the current claimant's labels.
	routes += ` AND NOT EXISTS (SELECT 1 FROM claim_requirement_terms term WHERE term.team=matched.team AND term.requirements=matched.requirements AND NOT EXISTS (SELECT 1 FROM claim_requirement_terms alt WHERE alt.team=term.team AND alt.requirements=term.requirements AND alt.term=term.term AND alt.label IN (SELECT value FROM labels)))`
	return `labels(value) AS MATERIALIZED (SELECT ` + q.text("label.value") + ` FROM ` + q.array(labels, "label") + `),matched AS MATERIALIZED (SELECT DISTINCT team,requirements FROM claim_requirement_terms WHERE label IN (SELECT value FROM labels)),routes AS MATERIALIZED (` + routes + `)`, nil
}

func (template *claimSQL) routesSQL(includeFresh bool) (string, []any, error) {
	q := claimSQL{dialect: template.dialect, route: true}
	clock := q.param(template.now)
	routes, err := q.requirementRoutes(template.facts[0])
	if err != nil {
		return "", nil, err
	}
	head := q.headSQL(template.kinds)
	freshHead := `NULL`
	if includeFresh {
		fresh := claimSQL{dialect: template.dialect, route: true, freshOnly: true}
		freshHead = fresh.headSQL(template.kinds)
		q.args = append(q.args, fresh.args...)
	}
	return `WITH clock(now) AS (SELECT CAST(` + clock + ` AS BIGINT)),` + routes + ` SELECT route.team,route.requirements,route.ordinal,` + head + `,` + freshHead + ` FROM routes route`, q.args, nil
}

func (template *claimSQL) freshTeamsSQL(demand UnitDemand) (string, []any, error) {
	q := claimSQL{dialect: template.dialect, route: true, freshOnly: true, target: demand.Target}
	if template.facts[0].Cloud == 0 {
		q.team = template.facts[0].Team
	}
	clock := q.param(template.now)
	routes, err := q.requirementRoutes(template.facts[0])
	if err != nil {
		return "", nil, err
	}
	head := q.headSQL(template.kinds)
	return `WITH clock(now) AS (SELECT CAST(` + clock + ` AS BIGINT)),` + routes + ` SELECT DISTINCT route.team FROM routes route WHERE ` + head + ` IS NOT NULL`, q.args, nil
}

func decodeClaimHead(head *claimHead, raw string) error {
	var row []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		return err
	}
	if len(row) != 6 {
		return errors.New("invalid claim route head")
	}
	for i, dest := range []any{&head.Team, &head.Unit.RunID, &head.Unit.NodeID, &head.Unit.SourceGeneration, &head.Unit.Kind, &head.Arrived} {
		if err := json.Unmarshal(row[i], dest); err != nil {
			return err
		}
	}
	return nil
}

func searchClaimTx(ctx context.Context, tx *storeTx, demand UnitDemand, template *claimSQL) (UnitRef, error) {
	fresh, err := freshClaimWorkTx(ctx, tx, demand, template)
	if err != nil {
		return UnitRef{}, err
	}
	query, args, err := template.routesSQL(fresh)
	if err != nil {
		return UnitRef{}, err
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return UnitRef{}, err
	}
	var heads []claimHead
	freshTeams := map[string]bool{}
	for rows.Next() {
		var head claimHead
		var raw, fresh sql.NullString
		if err = rows.Scan(&head.Team, &head.Requirements, &head.Ordinal, &raw, &fresh); err != nil {
			return UnitRef{}, errors.Join(err, rows.Close())
		}
		if fresh.Valid {
			freshTeams[string(head.Team)] = true
		}
		if !raw.Valid {
			continue
		}
		if err = decodeClaimHead(&head, raw.String); err != nil {
			return UnitRef{}, errors.Join(err, rows.Close())
		}
		heads = append(heads, head)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return UnitRef{}, err
	}
	if err := recordQueuedAsksTx(ctx, tx, demand, template, freshTeams); err != nil {
		return UnitRef{}, err
	}
	for len(heads) > 0 {
		slices.SortFunc(heads, compareClaimHeads)
		head := heads[0]
		heads = heads[1:]
		named := demand
		named.Target = &head.Unit
		q := claimSQL{dialect: template.dialect, policy: template.policy, overflow: template.overflow}
		facts := claimFactsForTeams(template.facts, map[string]bool{string(head.Team): true})
		q.placementFree, q.preferencesFree, err = claimPlacementFreeTx(ctx, tx, head.Team, head.Unit, template.policy)
		if err != nil {
			return UnitRef{}, err
		}
		teams := slices.DeleteFunc(slices.Clone(template.teams), func(f claimTeamFacts) bool { return f.Team != string(head.Team) })
		query, err = q.query(facts, teams, template.rates, []capability.UnitKind{head.Unit.Kind}, named, template.now)
		if err != nil {
			return UnitRef{}, err
		}
		unit, err := queryClaimTx(ctx, tx, &q, query)
		if err == nil {
			return unit, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return UnitRef{}, err
		}
		// Rejected heads advance only this transaction's merge of indexed routes.
		next := claimSQL{dialect: template.dialect, route: true, after: &head}
		clock := next.param(template.now)
		team := next.param(string(head.Team))
		requirements := next.param(head.Requirements)
		query = `WITH clock(now) AS (SELECT CAST(` + clock + ` AS BIGINT)),route(team,requirements) AS (SELECT ` + team + `,` + requirements + `) SELECT ` + next.headSQL(template.kinds) + ` FROM route`
		var raw sql.NullString
		if err = tx.QueryRowContext(ctx, query, next.args...).Scan(&raw); err != nil {
			return UnitRef{}, err
		}
		if raw.Valid {
			if err = decodeClaimHead(&head, raw.String); err != nil {
				return UnitRef{}, err
			}
			heads = append(heads, head)
		}
	}
	return UnitRef{}, notFound("eligible work", "")
}

// Placement-free work needs no ranking or preference relation in the SQL plan.
func claimPlacementFreeTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, policy ClaimPlacement) (bool, bool, error) {
	var request, execution []byte
	if err := tx.QueryRowContext(ctx, `SELECT r.request_json,COALESCE(n.execution_json,r.request_json) FROM runs r LEFT JOIN nodes n ON n.team=r.team AND n.run_id=r.id AND n.node_id=? WHERE r.team=? AND r.id=?`, unit.NodeID, team, unit.RunID).Scan(&request, &execution); err != nil {
		return false, false, err
	}
	var sealed struct {
		Request runspec.Request `json:"request"`
	}
	var settings runspec.JobExecutionSettings
	if err := json.Unmarshal(request, &sealed); err != nil {
		return false, false, err
	}
	if err := json.Unmarshal(execution, &settings); err != nil {
		return false, false, err
	}
	preferencesFree := unit.Kind != capability.UnitNode || sealed.Request.Burst == nil && len(settings.Prefers.Value) == 0 && len(policy.Preferences) == 0
	return preferencesFree && !runAnywhere(sealed.Request), preferencesFree, nil
}
