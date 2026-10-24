package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// Each route is an indexed requirement set, not a remembered scan position.
// Its head is re-evaluated by the same predicate used for named claims.
type claimHead struct {
	Team         Team
	Context      string
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

func (q *claimSQL) boundRunScope(current claimFacts, target *UnitRef) string {
	run := current.BindRun
	if target != nil {
		run = string(target.RunID)
	}
	if run != "" {
		query := `br.id=` + q.param(run)
		if current.Cloud == 0 {
			query += ` AND br.team=` + q.param(current.Team)
		}
		return query
	}
	if len(current.Pushes) > 0 {
		p := current.Pushes[0]
		scope := `br.team=` + q.param(current.Team) + ` AND br.repo_key=` + q.param(p.RepositoryKey) + ` AND br.provenance_sha=` + q.param(p.SHA) + ` AND br.provenance_ref=` + q.param(p.Ref)
		raw, _ := json.Marshal(p)
		return scope + ` AND EXISTS (SELECT 1 FROM (SELECT ` + q.param(string(raw)) + ` AS value) bound_push WHERE ` + q.pushScopeMatches(q.at(q.blob("br.request_json"), "provenance"), "bound_push.value", "br.repo_key") + `)`
	}
	return ""
}

func (q *claimSQL) sourceScope(alias string, kind capability.UnitKind) string {
	var conditions []string
	if q.heads != nil {
		conditions = append(conditions, `sh.kind=`+q.param(string(kind)))
	}
	if q.team != "" {
		conditions = append(conditions, alias+`.team=`+q.param(q.team))
	}
	if q.route {
		conditions = append(conditions, alias+`.team=route.team`)
		if kind == capability.UnitEval {
			conditions = append(conditions, `route.requirements=''`, `route.context=''`, `n.team=route.team`)
		} else {
			conditions = append(conditions, alias+`.claim_requirements=route.requirements`, alias+`.claim_context=route.context`)
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
	if q.candidate {
		conditions = append(conditions, alias+`.team=candidate.team`)
		column := `run_id`
		if alias == "t" {
			column = `id`
		}
		conditions = append(conditions, alias+`.`+column+`=candidate.run_id`)
		if alias == "n" {
			conditions = append(conditions, `n.node_id=candidate.node_id`)
		}
		if alias == "e" {
			conditions = append(conditions, `e.source_node_id=candidate.node_id`, `e.source_generation=candidate.source_generation`)
		}
	}
	if q.search && kind != capability.UnitEval {
		conditions = append(conditions, alias+`.claim_requirements=`+q.param(q.requirement))
		if q.context != "" {
			conditions = append(conditions, alias+`.claim_context=`+q.param(q.context))
		}
	}
	if q.runScope != "" {
		run := alias + `.run_id`
		if alias == "t" {
			run = alias + `.id`
		}
		conditions = append(conditions, run+` IN (SELECT id FROM bound_runs)`)
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
		if q.ordinal != nil {
			clause += ` AND tm.claim_award_ordinal=` + q.param(*q.ordinal)
		}
		if len(current.Excluded) > 0 {
			raw, _ := json.Marshal(current.Excluded)
			clause += ` AND tm.name NOT IN (SELECT ` + q.text("excluded.value") + ` FROM ` + q.array(q.param(string(raw)), "excluded") + `)`
		}
		return clause
	}
	routes := `SELECT tm.name AS team,'' AS requirements,tm.claim_award_ordinal AS ordinal FROM teams tm WHERE tm.name IN (` + liveTeamsSQL + `) AND ` + teamScope() + ` UNION ALL SELECT tm.name,matched.requirements,tm.claim_award_ordinal FROM matched JOIN teams tm ON tm.name=matched.team WHERE tm.name IN (` + liveTeamsSQL + `) AND ` + teamScope()
	// Every term must have an alternative in the current claimant's labels.
	routes += ` AND ` + q.requirementsMatchSQL("matched.team", "matched.requirements", func(alt string) string { return alt + ` IN (SELECT value FROM labels)` })
	return `labels(value) AS MATERIALIZED (SELECT ` + q.text("label.value") + ` FROM ` + q.array(labels, "label") + `),matched AS MATERIALIZED (SELECT DISTINCT team,requirements FROM claim_requirement_terms WHERE label IN (SELECT value FROM labels)),routes AS MATERIALIZED (` + routes + `)`, nil
}

func (template *claimSQL) routesSQL() (string, []any, error) {
	q := claimSQL{dialect: template.dialect, route: true, team: template.team, ordinal: template.ordinal}
	clock := q.param(template.now)
	routes, err := q.requirementRoutes(template.facts[0])
	if err != nil {
		return "", nil, err
	}
	kinds := make([]string, 0, len(template.kinds))
	for _, kind := range template.kinds {
		kinds = append(kinds, q.param(string(kind)))
	}
	contexts := `,contexts AS MATERIALIZED (SELECT routes.*,cc.id AS context FROM routes JOIN claim_contexts cc ON cc.team=routes.team AND cc.requirements=routes.requirements WHERE cc.unit_kind IN (` + strings.Join(kinds, ",") + `)`
	if slices.Contains(template.kinds, capability.UnitEval) {
		contexts += ` UNION ALL SELECT routes.*,'' AS context FROM routes WHERE routes.requirements=''`
	}
	contexts += `)`
	head := q.headSQL(template.kinds)
	return `WITH clock(now) AS (SELECT CAST(` + clock + ` AS BIGINT)),` + routes + contexts + ` SELECT route.team,route.requirements,route.ordinal,route.context,` + head + ` FROM contexts route CROSS JOIN clock`, q.args, nil
}

func decodeClaimHead(head *claimHead, raw string) error {
	row := []any{&head.Team, &head.Unit.RunID, &head.Unit.NodeID, &head.Unit.SourceGeneration, &head.Unit.Kind, &head.Arrived}
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		return err
	}
	if len(row) != 6 {
		return errors.New("invalid claim route head")
	}
	return nil
}

func searchClaimTx(ctx context.Context, tx *storeTx, demand UnitDemand, template *claimSQL) (UnitRef, error) {
	current := template.facts[0]
	if current.BindRun != "" || len(current.Pushes) > 0 {
		query, err := template.query(template.facts, template.teams, template.rates, template.kinds, demand, template.now)
		if err != nil {
			return UnitRef{}, err
		}
		return queryClaimTx(ctx, tx, template, query)
	}
	// Tied shares still compete by arrival in one indexed lookup.
	for remaining := template.teams; len(remaining) > 0; {
		ordinal := remaining[0].Ordinal
		end := 1
		for end < len(remaining) && remaining[end].Ordinal == ordinal {
			end++
		}
		group := remaining[:end]
		remaining = remaining[end:]
		scoped := *template
		scoped.ordinal = &ordinal
		scoped.teams = nil
		scoped.facts = slices.Clone(template.facts)
		scoped.facts[0].Excluded = slices.Clone(current.Excluded)
		for _, team := range group {
			if current.Cloud == 0 && current.Team != team.Team || slices.Contains(current.Excluded, team.Team) {
				continue
			}
			runnable, err := claimTeamRunnableTx(ctx, tx, Team(team.Team), template)
			if err != nil {
				return UnitRef{}, err
			}
			if runnable {
				scoped.teams = append(scoped.teams, team)
			} else {
				scoped.facts[0].Excluded = append(scoped.facts[0].Excluded, team.Team)
			}
		}
		if len(scoped.teams) == 0 {
			continue
		}
		if len(scoped.teams) == 1 {
			scoped.team = scoped.teams[0].Team
		}
		head, err := searchClaimRoutesTx(ctx, tx, demand, &scoped)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return UnitRef{}, err
		}
		return head.Unit, nil
	}
	return UnitRef{}, notFound("eligible work", "")
}

func searchClaimRoutesTx(ctx context.Context, tx *storeTx, demand UnitDemand, template *claimSQL) (claimHead, error) {
	heads, err := claimRouteHeadsTx(ctx, tx, template)
	if err != nil {
		return claimHead{}, err
	}
	held, err := rankingHeldContextsTx(ctx, tx, demand, template, heads)
	if err != nil {
		return claimHead{}, err
	}
	heldKeys := map[[2]string]bool{}
	for _, h := range held {
		heldKeys[[2]string{string(h.Team), h.Context}] = true
	}
	heads = slices.DeleteFunc(heads, func(h claimHead) bool { return heldKeys[[2]string{string(h.Team), h.Context}] })
	slices.SortFunc(heads, compareClaimHeads)
	var best *claimHead
	for _, route := range heads {
		if best != nil && compareClaimHeads(route, *best) >= 0 {
			break
		}
		template.placementFree, template.preferencesFree, err = claimPlacementFreeTx(ctx, tx, route.Team, route.Unit, template.policy)
		if err != nil {
			return claimHead{}, err
		}
		head, err := firstEligibleRouteTx(ctx, tx, demand, template, route, []capability.UnitKind{route.Unit.Kind})
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return claimHead{}, err
		}
		if best == nil || compareClaimHeads(head, *best) < 0 {
			best = &head
		}
	}
	if best == nil {
		return claimHead{}, notFound("eligible work", "")
	}
	return *best, nil
}

func claimRouteHeadsTx(ctx context.Context, tx *storeTx, template *claimSQL) ([]claimHead, error) {
	query, args, err := template.routesSQL()
	if err != nil {
		return nil, err
	}
	rows, err := tx.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var heads []claimHead
	for rows.Next() {
		var h claimHead
		var raw sql.NullString
		if err = rows.Scan(&h.Team, &h.Requirements, &h.Ordinal, &h.Context, &raw); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if !raw.Valid {
			continue
		}
		if err = decodeClaimHead(&h, raw.String); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		heads = append(heads, h)
	}
	return heads, errors.Join(rows.Err(), rows.Close())
}

func claimTeamRunnableTx(ctx context.Context, tx *storeTx, team Team, template *claimSQL) (bool, error) {
	current := template.facts[0]
	if current.Metered != 0 {
		for _, facts := range template.teams {
			if facts.Team != string(team) {
				continue
			}
			if facts.Held != 0 {
				return false, nil
			}
			var minPrice int64
			minClass := match.CloudSizes()[0].CreditClass
			for _, rate := range template.rates {
				if minPrice == 0 || rate.MicroPerSecond < minPrice {
					minPrice = rate.MicroPerSecond
				}
			}
			if facts.Balance < minPrice*SliceSeconds || current.Cloud != 0 && facts.Available < minClass {
				return false, nil
			}
		}
	}
	if len(current.Pools) > 0 && current.Launcher == 0 && current.Trust != "any" {
		if current.Trust != "" {
			return false, nil
		}
		q := claimSQL{dialect: template.dialect}
		q.args = append(q.args, team)
		pools, err := q.jsonParam(current.Pools)
		if err != nil {
			return false, err
		}
		var reserved bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM secrets s WHERE s.team=? AND `+q.contains(pools, "s.only_on")+`)`, q.args...).Scan(&reserved); err != nil {
			return false, err
		}
		if reserved {
			return false, nil
		}
	}
	if current.Cloud == 0 || len(template.overflow) == 0 {
		return true, nil
	}
	q := claimSQL{dialect: template.dialect}
	overflow, err := q.jsonParam(template.overflow)
	if err != nil {
		return false, err
	}
	q.args = append(q.args, team)
	cloud := `0`
	if current.Cloud != 0 {
		cloud = `1`
	}
	query := `WITH overflow_facts AS (SELECT ` + q.at("of.value", "team") + ` AS team,` + q.at("of.value", "account") + ` AS account,` + q.number(q.at("of.value", "used_seconds")) + ` AS used_seconds,` + q.at("of.value", "monthly_minutes") + ` AS monthly_minutes FROM ` + q.array(overflow, "of") + `) SELECT EXISTS(SELECT 1 FROM (SELECT r.team,r.created_principal,` + q.blob("r.request_json") + ` AS request FROM runs r WHERE r.team=? AND r.status IN ('pending','running') AND r.cancel_requested_at IS NULL AND r.intake_mode='execute') sized WHERE ` + q.overflowAllowedSQL("sized", cloud) + `)`
	var allowed bool
	err = tx.QueryRowContext(ctx, query, q.args...).Scan(&allowed)
	return allowed, err
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

func rankingHeldContextsTx(ctx context.Context, tx *storeTx, demand UnitDemand, template *claimSQL, heads []claimHead) ([]claimHead, error) {
	fresh := slices.DeleteFunc(slices.Clone(heads), func(h claimHead) bool {
		return h.Context == "" || h.Arrived <= template.now-int64(limits.RankingWindow)
	})
	if len(fresh) == 0 {
		return nil, nil
	}
	teams := map[string]bool{}
	for _, h := range fresh {
		teams[string(h.Team)] = true
	}
	facts, err := claimProofFactsTx(ctx, tx, fresh, template.facts, teams)
	if err != nil {
		return nil, err
	}
	if len(facts) == 1 {
		return nil, nil
	}
	funding := slices.DeleteFunc(slices.Clone(template.teams), func(t claimTeamFacts) bool { return !teams[t.Team] })
	q := claimSQL{dialect: template.dialect, policy: template.policy, overflow: template.overflow, heads: fresh, holdProof: true, preferencesFree: true}
	query, err := q.query(facts, funding, template.rates, template.kinds, demand, template.now)
	if err != nil {
		return nil, err
	}
	rows, err := tx.queryContext(ctx, query, q.args...)
	if err != nil {
		return nil, err
	}
	var held []claimHead
	for rows.Next() {
		var team Team
		var unit UnitRef
		if err = rows.Scan(&team, &unit.RunID, &unit.NodeID, &unit.SourceGeneration, &unit.Kind); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		for _, h := range fresh {
			if h.Team == team && h.Unit == unit {
				held = append(held, h)
				break
			}
		}
	}
	return held, errors.Join(rows.Err(), rows.Close())
}

// Disjoint labels otherwise multiply the SQL proof's claimant inputs.
func claimProofFactsTx(ctx context.Context, tx *storeTx, heads []claimHead, facts []claimFacts, teams map[string]bool) ([]claimFacts, error) {
	var labels map[string]bool
	if !slices.ContainsFunc(heads, func(h claimHead) bool { return h.Requirements == "" }) {
		// Requirement IDs cover the terms, so one tenant copy supplies each set.
		seen := map[string]bool{}
		var requirements []claimHead
		for _, head := range heads {
			if !seen[head.Requirements] {
				seen[head.Requirements] = true
				requirements = append(requirements, head)
			}
		}
		q := claimSQL{dialect: tx.dialect}
		input, err := q.jsonParam(requirements)
		if err != nil {
			return nil, err
		}
		rows, err := tx.queryContext(ctx, `SELECT DISTINCT term.label FROM `+q.array(input, "head")+` JOIN claim_requirement_terms term ON term.team=`+q.at("head.value", "Team")+` AND term.requirements=`+q.at("head.value", "Requirements"), q.args...)
		if err != nil {
			return nil, err
		}
		labels = map[string]bool{}
		for rows.Next() {
			var label string
			if err := rows.Scan(&label); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			labels[label] = true
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	selected := make([]claimFacts, 0, min(len(facts), len(heads)+1))
	for _, fact := range facts {
		if fact.Current == 0 {
			if fact.Cloud == 0 && !teams[fact.Team] || fact.FreeSlots <= 0 || fact.Personal == 1 || fact.HereRun != "" || fact.BindRun != "" || fact.Actions == 1 {
				continue
			}
			if labels != nil && !slices.ContainsFunc(fact.Labels, func(label string) bool { return labels[label] }) {
				continue
			}
		}
		selected = append(selected, fact)
	}
	return selected, nil
}

func firstEligibleRouteTx(ctx context.Context, tx *storeTx, demand UnitDemand, template *claimSQL, route claimHead, kinds []capability.UnitKind) (claimHead, error) {
	outer := claimSQL{dialect: template.dialect, team: string(route.Team), search: true, requirement: route.Requirements, context: route.Context}
	clock := outer.param(template.now)
	source := outer.source(kinds)
	teams := slices.DeleteFunc(slices.Clone(template.teams), func(team claimTeamFacts) bool { return team.Team != string(route.Team) })
	facts := claimFactsForTeams(template.facts, map[string]bool{string(route.Team): true})
	inner := claimSQL{dialect: template.dialect, policy: template.policy, overflow: template.overflow, candidate: true, placementFree: template.placementFree, preferencesFree: template.preferencesFree}
	queued := demand
	queued.Target = nil
	eligible, err := inner.query(facts, teams, template.rates, kinds, queued, template.now)
	if err != nil {
		return claimHead{}, err
	}
	order := `candidate.arrived,candidate.run_id`
	if len(kinds) != 1 || kinds[0] == capability.UnitNode || kinds[0] == capability.UnitEval {
		order += `,candidate.node_id`
	}
	if len(kinds) != 1 || kinds[0] == capability.UnitEval {
		order += `,candidate.source_generation`
	}
	query := `WITH clock(now) AS (SELECT CAST(` + clock + ` AS BIGINT)) SELECT candidate.team,candidate.run_id,candidate.node_id,candidate.source_generation,candidate.kind,candidate.arrived FROM (` + source + `) candidate WHERE EXISTS (` + eligible + `) ORDER BY ` + order + ` LIMIT 1`
	args := append(outer.args, inner.args...)
	head := route
	err = tx.QueryRowContext(ctx, query, args...).Scan(&head.Team, &head.Unit.RunID, &head.Unit.NodeID, &head.Unit.SourceGeneration, &head.Unit.Kind, &head.Arrived)
	if errors.Is(err, sql.ErrNoRows) {
		return claimHead{}, notFound("eligible work", "")
	}
	return head, err
}
