package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type claimRepositoryPattern struct {
	Key     string `json:"key"`
	Like    string `json:"like"`
	Slashes int    `json:"slashes"`
}

type claimFacts struct {
	Current        int                        `json:"current"`
	ObservedAt     int64                      `json:"observed_at"`
	Prefix         string                     `json:"prefix"`
	Team           string                     `json:"team"`
	Name           string                     `json:"name"`
	HereRun        string                     `json:"here_run"`
	BindRun        string                     `json:"bind_run"`
	BindKind       string                     `json:"bind_kind"`
	BindHolder     string                     `json:"bind_holder"`
	BindGeneration int64                      `json:"bind_generation"`
	ClaimBy        int64                      `json:"claim_by"`
	Here           int                        `json:"here"`
	Cloud          int                        `json:"cloud"`
	Launcher       int                        `json:"launcher"`
	Metered        int                        `json:"metered"`
	Personal       int                        `json:"personal"`
	Account        string                     `json:"account"`
	Tier           string                     `json:"tier"`
	Trust          string                     `json:"trust"`
	Actions        int                        `json:"actions"`
	Kinds          []string                   `json:"kinds"`
	Labels         []string                   `json:"labels"`
	Pools          []string                   `json:"pools"`
	Scopes         [][]claimRepositoryPattern `json:"scopes"`
	Accept         []claimRepositoryPattern   `json:"accept"`
	Filter         int                        `json:"filter"`
	Pushes         []capability.PushScope     `json:"pushes"`
	Excluded       []string                   `json:"excluded"`
}

func claimFactsForTeams(facts []claimFacts, teams map[string]bool) []claimFacts {
	var scoped []claimFacts
	for _, fact := range facts {
		if fact.Current == 1 || fact.Cloud == 1 || teams[fact.Team] {
			scoped = append(scoped, fact)
		}
	}
	return scoped
}

func claimPatterns(scope []string) []claimRepositoryPattern {
	out := make([]claimRepositoryPattern, 0, len(scope))
	for _, pattern := range scope {
		v := claimRepositoryPattern{Key: pattern}
		if normalized, err := sourceurl.ParseRepoPattern(pattern); err == nil {
			v.Like = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_", "*", "%").Replace(normalized)
			v.Slashes = strings.Count(normalized, "/")
		}
		out = append(out, v)
	}
	return out
}

func claimFactsTx(ctx context.Context, tx *storeTx, demand UnitDemand) (claimFacts, error) {
	var f claimFacts
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}, false)
	if err != nil {
		return f, err
	}
	leaf, root := chain[0].principal.Capability, chain[len(chain)-1].principal.Capability
	observed, err := claimantFactsTx(ctx, tx, chain, runspec.Request{})
	if err != nil {
		return f, err
	}
	profile := match.Profile{}
	if tx.store.claimProfiles != nil {
		profile, err = tx.store.claimProfiles.ResolveClaimProfile(ctx, chain[0].principal)
		if err != nil {
			return f, err
		}
	}
	f.Prefix, f.Team, f.Name = leaf.Prefix, leaf.Team, leaf.Subject.ID
	f.Cloud, f.Launcher, f.Metered = boolInt(capability.CloudLauncher(leaf)), boolInt(launcherClaimant(leaf)), boolInt(meteredPlacement(leaf, root))
	f.Personal, f.Account, f.Tier, f.Trust = boolInt(observed.Personal), observed.OwnerAccount, observed.Tier, string(root.Trust)
	f.BindRun, f.BindKind, f.BindHolder, f.BindGeneration = leaf.Binding.RunID, string(leaf.Binding.Kind), leaf.Binding.Holder, leaf.Binding.Generation
	if leaf.Binding.ClaimBy != nil {
		f.ClaimBy = leaf.Binding.ClaimBy.UnixNano()
	}
	f.Here = boolInt(leaf.Purpose == capability.PurposeHere)
	for _, node := range chain {
		c := node.principal.Capability
		if c.Purpose == capability.PurposeHere {
			f.HereRun = c.Binding.RunID
		}
		if c.Repositories != nil {
			f.Scopes = append(f.Scopes, claimPatterns(c.Repositories))
		}
		if c.Binding.Kind == capability.BindingPush {
			f.Actions = 1
			if c.Binding.Push == nil {
				return f, bindingRefusal()
			}
			f.Pushes = append(f.Pushes, *c.Binding.Push)
		}
	}
	profile = authenticatedClaimProfile(leaf, profile, f.HereRun != "")
	profile.Labels, profile.OS, profile.Arch = match.SelfAsserted(demand.Labels), demand.OS, demand.Arch
	f.Labels = slices.DeleteFunc(profile.Labels, func(label string) bool {
		return profile.OS != "" && strings.HasPrefix(label, "os=") || profile.Arch != "" && strings.HasPrefix(label, "arch=")
	})
	for key, value := range map[string]string{"name": profile.Name, "class": profile.Class, "location": profile.Location, "os": profile.OS, "arch": profile.Arch} {
		if value != "" {
			f.Labels = append(f.Labels, key+"="+value)
		}
	}
	if profile.Class == match.ClassCoordinator {
		f.Labels = append(f.Labels, "local", "location=coordinator")
	}
	if f.Cloud == 1 {
		for _, size := range match.CloudSizes() {
			f.Labels = append(f.Labels, size.Label)
		}
	}
	f.Pools = slices.Clone(profile.ReservedPools)
	for _, pool := range f.Pools {
		f.Labels = append(f.Labels, "pool="+pool)
	}
	if profile.Accept != nil {
		f.Accept = claimPatterns(profile.Accept.Patterns())
		f.Filter = 1
	}
	for _, kind := range []capability.UnitKind{capability.UnitNode, capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		unit := UnitRef{Kind: kind, RunID: runspec.RunID(leaf.Binding.RunID)}
		if _, err := claimKinds(leaf, UnitDemand{Target: &unit}); err == nil {
			f.Kinds = append(f.Kinds, string(kind))
		}
	}
	if len(demand.Kinds) > 0 {
		f.Kinds = slices.DeleteFunc(f.Kinds, func(k string) bool { return !slices.Contains(demand.Kinds, capability.UnitKind(k)) })
	}
	if len(demand.ExcludedTeamLabels) > 0 {
		rows, err := tx.QueryContext(ctx, liveTeamsSQL+` ORDER BY name`)
		if err != nil {
			return f, err
		}
		for rows.Next() {
			var team string
			if err = rows.Scan(&team); err != nil {
				return f, errors.Join(err, rows.Close())
			}
			if slices.Contains(demand.ExcludedTeamLabels, admission.TeamNodeValue(demand.JobNamespace, team)) {
				f.Excluded = append(f.Excluded, team)
			}
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return f, err
		}
	}
	return f, nil
}

func recordClaimAwardTx(ctx context.Context, tx *storeTx, team Team) error {
	if _, err := tx.ExecContext(ctx, `UPDATE claim_awards SET ordinal=ordinal+1 WHERE singleton=1`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE teams SET claim_award_ordinal=(SELECT ordinal FROM claim_awards WHERE singleton=1) WHERE name=?`, team)
	return err
}

type claimSQL struct {
	placementFree   bool
	preferencesFree bool
	projected       bool
	freshOnly       bool
	target          *UnitRef
	team            string
	route           bool
	routeJoin       bool
	after           *claimHead
	facts           []claimFacts
	teams           []claimTeamFacts
	rates           CreditRateTable
	kinds           []capability.UnitKind
	now             int64
	dialect         Dialect
	args            []any
	askSQL          string
	askArgs         []any
	policy          ClaimPlacement
	overflow        []claimOverflowFacts
}

func (q *claimSQL) param(v any) string { q.args = append(q.args, v); return "?" }
func (q *claimSQL) jsonParam(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if string(raw) == "null" {
		raw = []byte("[]")
	}
	return q.param(string(raw)), nil
}

func (q *claimSQL) blob(v string) string {
	if q.dialect == DialectPostgres {
		return `convert_from(` + v + `,'UTF8')`
	}
	return `CAST(` + v + ` AS TEXT)`
}

func (q *claimSQL) at(v string, path ...string) string {
	if q.dialect == DialectPostgres {
		return `((` + v + `)::jsonb #>> '{` + strings.Join(path, ",") + `}')`
	}
	return `json_extract(` + v + `,'$.` + strings.Join(path, ".") + `')`
}

func (q *claimSQL) array(v, alias string) string {
	if q.dialect == DialectPostgres {
		return `jsonb_array_elements(COALESCE((` + v + `)::jsonb,'[]'::jsonb)) AS ` + alias + `(value)`
	}
	return `json_each(COALESCE(` + v + `,'[]')) AS ` + alias
}

func (q *claimSQL) text(v string) string {
	if q.dialect == DialectPostgres {
		return `(` + v + ` #>> '{}')`
	}
	return v
}
func (q *claimSQL) number(v string) string { return `CAST(COALESCE(` + v + `,'0') AS BIGINT)` }

func (q *claimSQL) contains(array, value string) string {
	return `EXISTS (SELECT 1 FROM ` + q.array(array, "item") + ` WHERE ` + q.text("item.value") + `=` + value + `)`
}

func (q *claimSQL) trim(value string) string {
	function := "trim"
	if q.dialect == DialectPostgres {
		function = "btrim"
	}
	return function + "(" + value + ",' \t\n\v\f\r\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000')"
}

func (q *claimSQL) pinned(selector string) string {
	return q.anyAlternative(selector, func(alt string) string {
		return `substr(` + alt + `,1,5)='pool=' OR substr(` + alt + `,1,5)='name=' OR substr(` + alt + `,1,6)='class='`
	})
}

func (q *claimSQL) anywhere(request string) string {
	at := func(path ...string) string { return q.at(request, append([]string{"request"}, path...)...) }
	return `COALESCE(` + at("mode") + `,'')='execute' AND COALESCE(` + at("dispatch") + `,'')='controller' AND ` + at("burst") + ` IS NULL AND (COALESCE(CAST(` + at("options", "runner", "set") + ` AS TEXT),'false') IN ('false','0') OR COALESCE(` + at("options", "runner", "value", "kind") + `,'')='any') AND NOT (` + q.pinned(at("options", "requires", "value")) + `)`
}

func (q *claimSQL) alternatives(term string) string {
	from := `json_each('['||replace(json_quote(` + term + `),',','","')||']') AS alternative`
	if q.dialect == DialectPostgres {
		from = `unnest(string_to_array(` + term + `,',')) AS alternative(value)`
	}
	return `(SELECT ` + q.trim("alternative.value") + ` AS alt FROM ` + from + `) alternatives`
}

func (q *claimSQL) terms(selector string, alternative func(string) string) string {
	term := q.text("term.value")
	return `NOT EXISTS (SELECT 1 FROM ` + q.array(selector, "term") + ` WHERE ` + q.trim(term) + `<>'' AND NOT EXISTS (SELECT 1 FROM ` + q.alternatives(term) + ` WHERE alt<>'' AND (` + alternative("alt") + `)))`
}
func (q *claimSQL) fact(name string) string { return q.at("c.facts", name) }
func (q *claimSQL) flag(name string) string { return q.number(q.fact(name)) }

func (q *claimSQL) predicate() string {
	f := q.fact
	n := q.flag
	w := func(path ...string) string { return q.at("w.request", append([]string{"request"}, path...)...) }
	horizon := fmt.Sprint(int64(q.policy.Liveness))
	if q.policy.Liveness <= 0 {
		horizon = `CASE WHEN ` + w("burst") + ` IS NOT NULL OR (` + `w.run_anywhere=1` + `) THEN ` + fmt.Sprint(int64(time.Minute)) + ` ELSE 0 END`
	}
	live := `(` + n("current") + `=1 OR ((` + horizon + `)>0 AND ` + n("observed_at") + `>=clock.now-(` + horizon + `)))`
	labels := q.terms("w.selectors", func(alt string) string { return q.contains(f("labels"), alt) })
	reservation := `(` + f("pools") + ` IS NULL OR NOT EXISTS (SELECT 1 FROM ` + q.array(f("pools"), "pool") + `) OR EXISTS (SELECT 1 FROM ` + q.array(f("pools"), "pool") + ` WHERE ` + q.contains("w.allowed_pools", q.text("pool.value")) + ` AND ` + q.anyAlternative("w.selectors", func(alt string) string { return alt + `='pool='||` + q.text("pool.value") }) + `))`
	scopes := `NOT EXISTS (SELECT 1 FROM ` + q.array(f("scopes"), "scope") + ` WHERE NOT EXISTS (SELECT 1 FROM ` + q.array("scope.value", "pattern") + ` WHERE (` + q.at("pattern.value", "key") + `=w.repo_key AND w.repo_key<>'') OR (` + q.at("pattern.value", "like") + `<>'' AND w.repository LIKE ` + q.at("pattern.value", "like") + ` ESCAPE '\' AND length(w.repository)-length(replace(w.repository,'/',''))=` + q.number(q.at("pattern.value", "slashes")) + `)))`
	accepted := `(` + n("filter") + `=0 OR (w.kind IN ('node','eval') AND w.repo_display='' AND COALESCE(` + w("git", "repository", "display") + `,'')='') OR EXISTS (SELECT 1 FROM ` + q.array(f("accept"), "pattern") + ` WHERE ` + q.at("pattern.value", "like") + `<>'' AND w.repository LIKE ` + q.at("pattern.value", "like") + ` ESCAPE '\' AND length(w.repository)-length(replace(w.repository,'/',''))=` + q.number(q.at("pattern.value", "slashes")) + `))`
	trust := `(` + f("trust") + `='any' OR (` + f("trust") + `='' AND NOT EXISTS (SELECT 1 FROM secrets s WHERE s.team=w.team AND ` + q.contains(f("pools"), "s.only_on") + `)) OR (` + q.at("w.request", "provenance", "trust") + ` IN ('verified-default','schedule') AND (` + f("pools") + ` IS NULL OR NOT EXISTS (SELECT 1 FROM ` + q.array(f("pools"), "pool") + `) OR ` + n("launcher") + `=1)))`
	bind := `(` + f("bind_run") + `='' OR (w.run_id=` + f("bind_run") + ` AND (` + n("here") + `=0 OR (w.kind='trigger' AND EXISTS (SELECT 1 FROM triggers bt WHERE bt.team=w.team AND bt.id=w.run_id AND (bt.generation<>0 OR ` + n("claim_by") + `>clock.now))))))`
	pushes := `NOT EXISTS (SELECT 1 FROM ` + q.array(f("pushes"), "push") + ` WHERE COALESCE(` + q.at("push.value", "workflow_run_id") + `,'')='' OR ` + q.at("push.value", "repo_key") + `<>w.repo_key OR ` + q.at("push.value", "ref") + `<>COALESCE(` + q.at("w.request", "provenance", "ref") + `,'') OR ` + q.at("push.value", "sha") + `<>COALESCE(` + q.at("w.request", "provenance", "sha") + `,''))`
	personal := `(` + n("personal") + `=0 OR (` + f("account") + `<>'' AND ` + f("account") + `=w.requester AND NOT EXISTS (SELECT 1 FROM ancestors a WHERE a.team=w.team AND a.child=w.run_id AND a.origin IN ('github_app','schedule'))))`
	runnerKind := `COALESCE(` + w("options", "runner", "value", "kind") + `,'')`
	runnerValue := `COALESCE(` + w("options", "runner", "value", "value") + `,'')`
	runner := `(` + runnerKind + `<>'here' OR ` + f("here_run") + `=w.run_id OR (` + w("burst") + ` IS NOT NULL AND w.kind='node')) AND (` + runnerKind + `<>'name' OR (` + runnerValue + `<>'' AND ` + f("name") + `=` + runnerValue + `)) AND (` + runnerKind + `<>'github-actions' OR w.kind NOT IN ('trigger','plan') OR ` + n("actions") + `=1) AND (` + runnerKind + `<>'label' OR (` + runnerValue + `<>''))`
	burst := `(` + w("burst") + ` IS NULL OR (` + f("here_run") + `=w.run_id AND w.kind<>'node') OR EXISTS (SELECT 1 FROM ` + q.array(w("burst", "preferences"), "step") + ` WHERE (` + q.at("step.value", "target") + `='cloud' AND ` + n("cloud") + `=1) OR (` + q.at("step.value", "target") + `='here' AND ` + f("here_run") + `=w.run_id) OR (` + q.at("step.value", "target") + `='machines' AND ` + n("personal") + `=1) OR ((` + n("personal") + `=1 OR ` + f("here_run") + `=w.run_id) AND ` + q.contains(f("labels"), q.at("step.value", "target")) + `)))`
	guards := `(w.kind NOT IN ('node','eval') OR ` + `CAST(` + q.at("w.spec", "resolved", "guards", "passed") + ` AS TEXT) IN ('true','1') OR (COALESCE(` + q.at("w.spec", "resolved", "guards", "when") + `,'[]')='[]' AND COALESCE(` + q.at("w.spec", "resolved", "guards", "unless") + `,'[]')='[]' AND COALESCE(` + q.at("w.spec", "resolved", "guards", "reason") + `,'')=''))`
	return strings.Join([]string{
		labels, live, `(` + n("cloud") + `=1 OR w.team=` + f("team") + `)`, q.contains(f("kinds"), "w.kind"), `NOT (` + q.contains(f("excluded"), "w.team") + `)`, bind, pushes, scopes, accepted, trust, personal, runner, burst, guards, reservation,
	}, " AND ")
}

func (q *claimSQL) anyAlternative(selector string, test func(string) string) string {
	term := q.text("term.value")
	return `EXISTS (SELECT 1 FROM ` + q.array(selector, "term") + ` WHERE EXISTS (SELECT 1 FROM ` + q.alternatives(term) + ` WHERE alt<>'' AND (` + test("alt") + `)))`
}

type claimTeamFacts struct {
	Team      string `json:"team"`
	Available int64  `json:"available"`
	Balance   int64  `json:"balance"`
	Held      int    `json:"held"`
}

func claimTeamFactsTx(ctx context.Context, tx *storeTx, current claimFacts) ([]claimTeamFacts, error) {
	query := liveTeamsSQL
	var args []any
	if current.Cloud == 0 {
		query += ` AND name=?`
		args = append(args, current.Team)
	}
	rows, err := tx.QueryContext(ctx, query+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	var teams []Team
	for rows.Next() {
		var team Team
		if err := rows.Scan(&team); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		teams = append(teams, team)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	out := make([]claimTeamFacts, 0, len(teams))
	for _, team := range teams {
		standing, err := trustStandingTx(ctx, tx, team, tx.now(ctx))
		if err != nil {
			return nil, err
		}
		balance, err := creditBalanceTx(ctx, tx, team)
		if err != nil {
			return nil, err
		}
		var active int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(c.cpu_class),0) FROM billing_awards a JOIN credit_charges c ON c.team=a.team AND c.capability_prefix=a.capability_prefix AND c.kind='reservation' WHERE a.team=? AND a.executor_class='cloud' AND a.settled_at IS NULL`, team).Scan(&active); err != nil {
			return nil, err
		}
		out = append(out, claimTeamFacts{Team: string(team), Available: standing.CloudVCPU - active, Balance: balance, Held: boolInt(standing.HoldCause != "" || standing.Tier == "Held")})
	}
	return out, nil
}

func (o *Operator) claimObserversTx(ctx context.Context, tx *storeTx, current claimFacts) ([]claimFacts, error) {
	policy, _ := ClaimPlacementFromContext(ctx)
	horizon := policy.Liveness
	if horizon <= 0 {
		horizon = time.Minute
	}
	now := tx.now(ctx)
	rows, err := tx.QueryContext(ctx, `SELECT prefix,claim_observation_json FROM capabilities WHERE claim_observation_json IS NOT NULL AND last_used_at>=? AND prefix<>? ORDER BY prefix`, now.Add(-horizon).UnixNano(), current.Prefix)
	if err != nil {
		return nil, err
	}
	var polls []runnerPoll
	for rows.Next() {
		var prefix string
		var raw []byte
		var poll runnerPoll
		if err = rows.Scan(&prefix, &raw); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if err = json.Unmarshal(raw, &poll); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		poll.CapabilityPrefix = prefix
		if poll.ObservedAt.After(now) || poll.ObservedAt.Before(now.Add(-horizon)) {
			continue
		}
		polls = append(polls, poll)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	facts := []claimFacts{current}
	for _, poll := range polls {
		f, err := claimFactsTx(ctx, tx, poll.UnitDemand)
		if refusal(err) || unavailableRunner(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		f.ObservedAt = poll.ObservedAt.UnixNano()
		facts = append(facts, f)
	}
	return facts, nil
}

func (q *claimSQL) source(kinds []capability.UnitKind) string {
	var queries []string
	for _, kind := range kinds {
		switch kind {
		case capability.UnitNode:
			// A private projection precedes readiness and never reserves work.
			if q.projected {
				queries = append(queries, `SELECT n.team,n.run_id,n.node_id,0 AS source_generation,COALESCE(n.ready_at,clock.now) AS arrived,'node' AS kind,`+q.blob("n.execution_json")+` AS execution FROM nodes n,clock WHERE `+q.sourceScope("n", kind))
				continue
			}
			from := `nodes n,clock`
			if q.freshOnly || q.route {
				from = `clock CROSS JOIN `
				if q.routeJoin {
					from += `routes route CROSS JOIN `
				}
				from += `nodes n`
			}
			queries = append(queries, `SELECT n.team,n.run_id,n.node_id,0 AS source_generation,n.ready_at AS arrived,'node' AS kind,`+q.blob("n.execution_json")+` AS execution FROM `+from+` WHERE `+q.sourceScope("n", kind)+` AND n.status='pending' AND n.ready_at IS NOT NULL AND n.ready_at<=clock.now AND n.claimed_by IS NULL AND n.capability_prefix='' AND n.cancel_requested_at IS NULL AND NOT EXISTS (SELECT 1 FROM runs dr WHERE dr.team=n.team AND dr.id=n.run_id AND dr.claim_wait_ns>0 AND (n.queue_started_at IS NULL OR n.queue_started_at<=clock.now-dr.claim_wait_ns))`)
		case capability.UnitEval:
			from := `eval_units e JOIN nodes n ON n.team=e.team AND n.run_id=e.run_id AND n.node_id=e.source_node_id`
			clockJoin := `,clock`
			if ((q.freshOnly || q.route) && q.dialect == DialectSQLite) || q.routeJoin {
				// The readiness range must drive the join before pending eval units.
				prefix := `clock CROSS JOIN `
				if q.routeJoin {
					prefix += `routes route CROSS JOIN `
				}
				if q.dialect == DialectSQLite {
					from = prefix + `nodes n CROSS JOIN eval_units e ON n.team=e.team AND n.run_id=e.run_id AND n.node_id=e.source_node_id`
				} else {
					from = prefix + from
				}
				clockJoin = ``
			}
			queries = append(queries, `SELECT e.team,e.run_id,e.source_node_id AS node_id,e.source_generation,n.finished_at AS arrived,'eval' AS kind,NULL AS execution FROM `+from+` JOIN leases l ON l.name=e.owner_lease_name`+clockJoin+` WHERE `+q.sourceScope("e", kind)+` AND e.status='pending' AND e.claimed_by IS NULL AND e.capability_prefix='' AND (e.expires_at IS NULL OR e.expires_at>clock.now) AND l.name='controller.owner' AND l.until>clock.now AND n.generation=e.source_generation AND n.outcome IN ('success','memoized') AND n.expansion='expected'`)
		default:
			from := `triggers t JOIN runs tr ON tr.team=t.team AND tr.id=t.id,clock`
			if q.freshOnly || q.route {
				from = `clock CROSS JOIN `
				if q.routeJoin {
					from += `routes route CROSS JOIN `
				}
				from += `triggers t JOIN runs tr ON tr.team=t.team AND tr.id=t.id`
			}
			queries = append(queries, `SELECT t.team,t.id AS run_id,'' AS node_id,0 AS source_generation,t.available_at AS arrived,`+q.param(string(kind))+` AS kind,NULL AS execution FROM `+from+` WHERE `+q.sourceScope("t", kind)+` AND t.unit_kind=`+q.param(string(kind))+` AND t.status='pending' AND t.claimed_by IS NULL AND t.capability_prefix='' AND t.cancel_requested_at IS NULL AND t.available_at<=clock.now AND (t.expires_at IS NULL OR t.expires_at>clock.now) AND (t.started_at IS NOT NULL OR tr.claim_wait_ns<=0 OR tr.created_at>clock.now-tr.claim_wait_ns)`)
		}
	}
	return strings.Join(queries, ` UNION ALL `)
}

func (q *claimSQL) query(facts []claimFacts, teams []claimTeamFacts, rates CreditRateTable, kinds []capability.UnitKind, demand UnitDemand, now int64) (string, error) {
	q.facts = facts
	q.target = demand.Target
	q.team = ""
	if len(facts) > 0 && facts[0].Cloud == 0 {
		q.team = facts[0].Team
	} else if len(teams) == 1 {
		q.team = teams[0].Team
	}
	clock := `CAST(` + q.param(now) + ` AS BIGINT)`
	factsParam, err := q.jsonParam(facts)
	if err != nil {
		return "", err
	}
	teamsParam, err := q.jsonParam(teams)
	if err != nil {
		return "", err
	}
	overflowParam, err := q.jsonParam(q.overflow)
	if err != nil {
		return "", err
	}
	rateParam, err := q.jsonParam(rates)
	if err != nil {
		return "", err
	}
	routes := ""
	if q.freshOnly {
		q.route, q.routeJoin = true, true
		routes, err = q.requirementRoutes(facts[0])
		if err != nil {
			return "", err
		}
		routes += ","
	}
	ready := q.source(kinds)
	readyFilter := ""
	if demand.Target != nil {
		target := demand.Target
		readyFilter = ` WHERE run_id=` + q.param(string(target.RunID)) + ` AND node_id=` + q.param(string(target.NodeID)) + ` AND kind=` + q.param(string(target.Kind)) + ` AND source_generation=` + q.param(target.SourceGeneration)
	}
	if len(facts) > 0 && facts[0].Cloud == 0 {
		joiner := " WHERE "
		if readyFilter != "" {
			joiner = " AND "
		}
		readyFilter += joiner + `team=` + q.param(facts[0].Team)
	}
	request := q.blob("r.request_json")
	spec := q.blob("r.spec_json")
	options := `COALESCE(` + q.at(spec, "resolved", "settings", "options") + `,` + q.at(request, "request", "options") + `,'{}')`
	selectors := `CASE WHEN ready.kind='node' THEN COALESCE(` + q.at("ready.execution", "requires", "value") + `,'[]') ELSE COALESCE(` + q.at(options, "requires", "value") + `,'[]') END`
	runnerKind := q.at(request, "request", "options", "runner", "value", "kind")
	runnerValue := q.at(request, "request", "options", "runner", "value", "value")
	appended := `json_insert(` + selectors + `,'$[#]',` + runnerValue + `)`
	if q.dialect == DialectPostgres {
		appended = `((` + selectors + `)::jsonb || jsonb_build_array(` + runnerValue + `))::text`
	}
	selectors = `CASE WHEN ` + runnerKind + `='label' THEN ` + appended + ` ELSE ` + selectors + ` END`
	prefers := `CASE WHEN ready.kind='node' THEN COALESCE(` + q.at("ready.execution", "prefers", "value") + `,'[]') ELSE COALESCE(` + q.at(options, "prefers", "value") + `,'[]') END`
	repository := "r.repo_identity"
	class := `CAST(NULL AS BIGINT)`
	if slices.ContainsFunc(facts, func(f claimFacts) bool { return f.Metered == 1 }) {
		var sizes []string
		for ordinal, size := range match.CloudSizes() {
			sizes = append(sizes, `SELECT '`+size.Label+`' AS label,`+fmt.Sprint(size.CreditClass)+` AS cpu_class,`+fmt.Sprint(ordinal)+` AS ordinal`)
		}
		class = `(SELECT size.cpu_class FROM (` + strings.Join(sizes, ` UNION ALL `) + `) size WHERE ` + q.terms("unfunded.selectors", func(alt string) string {
			return `(` + alt + `=size.label OR (NOT (substr(` + alt + `,1,10)='sparkwing-' AND substr(` + alt + `,-3)='cpu') AND ` + q.contains(q.at("unfunded.facts", "labels"), alt) + `))`
		}) + ` ORDER BY size.ordinal LIMIT 1)`
	}
	raw := `SELECT ready.*,r.repo_key,r.repo_display,` + repository + ` AS repository,` + request + ` AS request,` + spec + ` AS spec,` + options + ` AS options,` + selectors + ` AS selectors,` + prefers + ` AS preferences,` + q.blob("r.allowed_pools") + ` AS allowed_pools,r.dispatch,r.created_principal,tm.claim_ranking,tm.claim_award_ordinal,
 CASE WHEN r.created_subject_kind='account' THEN r.created_principal WHEN r.created_subject_kind='machine' THEN (SELECT CASE WHEN COUNT(*)>0 AND COUNT(DISTINCT m.created_by)=1 AND MIN(CASE WHEN COALESCE(CAST(` + q.at(q.blob("m.authority_json"), "origin", "personal") + ` AS TEXT),'false') IN ('true','1') AND m.created_by<>'' THEN 1 ELSE 0 END)=1 THEN MIN(m.created_by) ELSE '' END FROM capabilities m WHERE m.team=r.team AND m.subject_id=r.created_principal AND m.subject_kind='machine' AND m.bind_kind='none' AND m.parent_id IS NULL) ELSE '' END AS requester
 FROM ready JOIN runs r ON r.team=ready.team AND r.id=ready.run_id JOIN teams tm ON tm.name=r.team,clock
 WHERE r.team IN (` + liveTeamsSQL + `) AND r.status IN ('pending','running') AND r.cancel_requested_at IS NULL AND r.intake_mode='execute' AND NOT EXISTS (SELECT 1 FROM triggers deadline WHERE deadline.team=r.team AND deadline.id=r.id AND deadline.expires_at<=clock.now)`
	ancestorOrigin := q.at(q.blob("ar.request_json"), "request", "origin")
	ancestorParent := `CASE WHEN ` + ancestorOrigin + `='retry' THEN ar.retry_of WHEN ` + ancestorOrigin + `='child' THEN COALESCE(ar.parent_run_id,'') ELSE '' END`
	funded := `(` + q.number(q.at("sized.facts", "metered")) + `=0 OR (sized.cpu_class IS NOT NULL AND ((rates.price IS NULL AND ` + q.number(q.at("sized.facts", "current")) + `=1) OR (tf.held=0 AND tf.balance>=rates.price*` + fmt.Sprint(SliceSeconds) + ` AND (` + q.number(q.at("sized.facts", "cloud")) + `=0 OR tf.available>=sized.cpu_class)))))`
	overflowAllowed := `(` + q.number(q.at("sized.facts", "cloud")) + `=0 OR ` + q.at("sized.request", "request", "burst") + ` IS NULL OR NOT EXISTS (SELECT 1 FROM overflow_facts of WHERE of.team=sized.team AND of.account=sized.created_principal AND ((of.monthly_minutes IS NOT NULL AND of.used_seconds+` + fmt.Sprint(SliceSeconds) + `>` + q.number("of.monthly_minutes") + `*60) OR (` + q.at("sized.request", "request", "burst", "overflow_minutes") + ` IS NOT NULL AND of.used_seconds+` + fmt.Sprint(SliceSeconds) + `>` + q.number(q.at("sized.request", "request", "burst", "overflow_minutes")) + `*60))))`
	claimants := `SELECT value FROM ` + q.array(factsParam, "fact")
	ctes := `WITH RECURSIVE clock(now) AS (SELECT ` + clock + `),claimants(facts) AS MATERIALIZED (` + claimants + `),
 team_facts AS MATERIALIZED (SELECT ` + q.at("tf.value", "team") + ` AS team,` + q.number(q.at("tf.value", "available")) + ` AS available,` + q.number(q.at("tf.value", "balance")) + ` AS balance,` + q.number(q.at("tf.value", "held")) + ` AS held FROM ` + q.array(teamsParam, "tf") + `),
 overflow_facts AS MATERIALIZED (SELECT ` + q.at("of.value", "team") + ` AS team,` + q.at("of.value", "account") + ` AS account,` + q.number(q.at("of.value", "used_seconds")) + ` AS used_seconds,` + q.at("of.value", "monthly_minutes") + ` AS monthly_minutes FROM ` + q.array(overflowParam, "of") + `),
 rates AS MATERIALIZED (SELECT ` + q.number(q.at("rate.value", "cores")) + ` AS cpu_class,` + q.number(q.at("rate.value", "micro_per_second")) + ` AS price FROM ` + q.array(rateParam, "rate") + `),
 ` + routes + `source(team,run_id,node_id,source_generation,arrived,kind,execution) AS MATERIALIZED (` + ready + `),ready AS MATERIALIZED (SELECT * FROM source ` + readyFilter + `),work_input AS MATERIALIZED (` + raw + `),work AS MATERIALIZED (SELECT base.*,CASE WHEN ` + q.anywhere("base.request") + ` THEN 1 ELSE 0 END AS run_anywhere,CASE WHEN ` + q.pinned("base.selectors") + ` THEN 1 ELSE 0 END AS pinned FROM work_input base),
 ancestors(team,child,id,origin,parent) AS (SELECT ar.team,ar.id,ar.id,` + ancestorOrigin + `,` + ancestorParent + ` FROM runs ar JOIN work aw ON aw.team=ar.team AND aw.run_id=ar.id UNION SELECT a.team,a.child,ar.id,` + ancestorOrigin + `,` + ancestorParent + ` FROM ancestors a JOIN runs ar ON ar.team=a.team AND ar.id=a.parent),
 unfunded AS MATERIALIZED (SELECT w.*,c.facts FROM work w CROSS JOIN claimants c,clock WHERE ` + q.predicate() + `),
 sized AS MATERIALIZED (SELECT unfunded.*,` + class + ` AS cpu_class FROM unfunded),
 eligible AS MATERIALIZED (SELECT sized.*,rates.price FROM sized LEFT JOIN rates ON rates.cpu_class=sized.cpu_class JOIN team_facts tf ON tf.team=sized.team WHERE ` + funded + ` AND ` + overflowAllowed + `)`
	placement, err := q.placementCTEs(q.policy)
	if err != nil {
		return "", err
	}
	ctes += placement
	q.askArgs = append(slices.Clone(q.args), demand.CapabilityPrefix)
	q.askSQL = ctes + ` INSERT INTO unit_asks(team,run_id,unit_kind,node_id,claimant,claim_rank,asked_at) SELECT w.team,w.run_id,w.kind,w.node_id,` + q.at("w.facts", "prefix") + `,w.claim_rank,clock.now FROM pairs w,clock WHERE ` + q.at("w.facts", "prefix") + `=? AND w.ranked=1 AND w.claim_rank>0 AND clock.now<w.arrived+` + fmt.Sprint(int64(limits.RankingWindow)) + ` AND EXISTS (SELECT 1 FROM pairs b WHERE ` + unitIdentitySQL("b", "w") + ` AND b.claim_rank>=0 AND b.claim_rank<w.claim_rank) ON CONFLICT(team,run_id,unit_kind,node_id,claimant) DO UPDATE SET claim_rank=excluded.claim_rank,asked_at=excluded.asked_at WHERE unit_asks.asked_at<excluded.asked_at-` + fmt.Sprint(int64(limits.RankingWindow))
	final := ` SELECT w.team,w.run_id,w.node_id,w.source_generation,w.kind FROM available w WHERE ` + q.at("w.facts", "prefix") + `=` + q.param(demand.CapabilityPrefix)
	if demand.Target != nil {
		target := demand.Target
		final += ` AND w.run_id=` + q.param(string(target.RunID)) + ` AND w.node_id=` + q.param(string(target.NodeID)) + ` AND w.kind=` + q.param(string(target.Kind)) + ` AND w.source_generation=` + q.param(target.SourceGeneration)
	}
	final += ` ORDER BY w.claim_award_ordinal,w.arrived,w.team,w.run_id,w.kind,w.node_id,w.source_generation LIMIT 1`
	return ctes + final, nil
}

func claimInputsTx(ctx context.Context, tx *storeTx, demand UnitDemand, kinds []capability.UnitKind, q *claimSQL) error {
	current, err := claimFactsTx(ctx, tx, demand)
	if err != nil {
		return err
	}
	current.Current = 1
	facts, err := tx.operator.claimObserversTx(ctx, tx, current)
	if err != nil {
		return err
	}
	teams, err := claimTeamFactsTx(ctx, tx, current)
	if err != nil {
		return err
	}
	rates, err := creditRateTableTx(ctx, tx)
	if err != nil {
		return err
	}
	q.overflow, err = tx.operator.claimOverflowFactsTx(ctx, tx, current.Team, current.Cloud == 1)
	if err != nil {
		return err
	}
	q.dialect = tx.dialect
	q.policy, _ = ClaimPlacementFromContext(ctx)
	q.facts, q.teams, q.rates, q.kinds, q.now = facts, teams, rates, kinds, tx.now(ctx).UnixNano()
	return nil
}

func claimQueryTx(ctx context.Context, tx *storeTx, demand UnitDemand, kinds []capability.UnitKind, q *claimSQL) (string, error) {
	if err := claimInputsTx(ctx, tx, demand, kinds, q); err != nil {
		return "", err
	}
	return q.query(q.facts, q.teams, q.rates, kinds, demand, q.now)
}

func queuedUnitTx(ctx context.Context, tx *storeTx, demand UnitDemand, kinds []capability.UnitKind) (UnitRef, error) {
	q := claimSQL{}
	err := claimInputsTx(ctx, tx, demand, kinds, &q)
	if err != nil {
		return UnitRef{}, err
	}
	if demand.Target != nil {
		team, found, err := tx.operator.runOwnerTx(ctx, tx, string(demand.Target.RunID))
		if err != nil {
			return UnitRef{}, err
		}
		if found {
			q.placementFree, q.preferencesFree, err = claimPlacementFreeTx(ctx, tx, team, *demand.Target, q.policy)
			if err != nil {
				return UnitRef{}, err
			}
		}
	}
	if demand.Target == nil {
		return searchClaimTx(ctx, tx, demand, &q)
	}
	if err := recordQueuedAsksTx(ctx, tx, demand, &q, nil); err != nil {
		return UnitRef{}, err
	}
	query, err := q.query(q.facts, q.teams, q.rates, kinds, demand, q.now)
	if err != nil {
		return UnitRef{}, err
	}
	return queryClaimTx(ctx, tx, &q, query)
}

func queryClaimTx(ctx context.Context, tx *storeTx, q *claimSQL, query string) (UnitRef, error) {
	var team Team
	var unit UnitRef
	err := tx.QueryRowContext(ctx, query, q.args...).Scan(&team, &unit.RunID, &unit.NodeID, &unit.SourceGeneration, &unit.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return UnitRef{}, notFound("eligible work", "")
	}
	return unit, err
}

func (q *claimSQL) indexed(v, alias string) string {
	if q.dialect == DialectPostgres {
		return `jsonb_array_elements(COALESCE((` + v + `)::jsonb,'[]'::jsonb)) WITH ORDINALITY AS ` + alias + `(value,ordinal)`
	}
	return q.array(v, alias)
}

func (q *claimSQL) index(alias string) string {
	if q.dialect == DialectPostgres {
		return alias + `.ordinal-1`
	}
	return `CAST(` + alias + `.key AS BIGINT)`
}

func unitIdentitySQL(a, b string) string {
	return a + `.team=` + b + `.team AND ` + a + `.run_id=` + b + `.run_id AND ` + a + `.kind=` + b + `.kind AND ` + a + `.node_id=` + b + `.node_id AND ` + a + `.source_generation=` + b + `.source_generation`
}

func (q *claimSQL) placementCTEs(policy ClaimPlacement) (string, error) {
	if q.placementFree {
		return `,pairs AS MATERIALIZED (SELECT e.*,0 AS ranked,0 AS claim_rank FROM eligible e),available AS MATERIALIZED (SELECT * FROM pairs)`, nil
	}
	var param string
	if !q.preferencesFree {
		var err error
		param, err = q.jsonParam(policy.Preferences)
		if err != nil {
			return "", err
		}
	}
	defaults, err := q.jsonParam(DefaultClaimRanking)
	if err != nil {
		return "", err
	}
	opts := func(path ...string) string { return q.at("e.request", append([]string{"request"}, path...)...) }
	ranked := `CASE WHEN e.run_anywhere=1 AND e.pinned=0 THEN 1 ELSE 0 END`
	tier := `CASE WHEN ` + q.number(q.at("e.facts", "personal")) + `=1 AND ` + q.at("e.facts", "name") + `=COALESCE(` + opts("origin_machine") + `,'') THEN 'here' ELSE ` + q.at("e.facts", "tier") + ` END`
	ranks := `CASE WHEN e.claim_ranking='' THEN ` + defaults + ` ELSE e.claim_ranking END`
	ranking := `COALESCE((SELECT ` + q.index("tier") + ` FROM ` + q.indexed(ranks, "tier") + ` WHERE ` + q.text("tier.value") + `=` + tier + ` LIMIT 1),-1)`
	same := func(a, b string) string { return unitIdentitySQL(a, b) }
	better := `EXISTS (SELECT 1 FROM pairs b WHERE ` + same("b", "w") + ` AND b.claim_rank>=0 AND b.claim_rank<w.claim_rank)`
	rankHold := `(w.claim_rank>0 AND clock.now<w.arrived+` + fmt.Sprint(int64(limits.RankingWindow)) + ` AND ` + better + `) OR (clock.now>=w.arrived+` + fmt.Sprint(int64(limits.RankingWindow)) + ` AND clock.now<w.arrived+` + fmt.Sprint(int64(limits.RankingWindow+limits.RankingAward)) + ` AND EXISTS (SELECT 1 FROM unit_asks ask WHERE ask.team=w.team AND ask.run_id=w.run_id AND ask.unit_kind=w.kind AND ask.node_id=w.node_id AND ask.asked_at>=w.arrived AND ask.asked_at<w.arrived+` + fmt.Sprint(int64(limits.RankingWindow)) + `) AND ` + q.at("w.facts", "prefix") + `<>(SELECT ask.claimant FROM unit_asks ask WHERE ask.team=w.team AND ask.run_id=w.run_id AND ask.unit_kind=w.kind AND ask.node_id=w.node_id AND ask.asked_at>=w.arrived AND ask.asked_at<w.arrived+` + fmt.Sprint(int64(limits.RankingWindow)) + ` ORDER BY ask.claim_rank,ask.asked_at,ask.claimant LIMIT 1))`
	if q.preferencesFree {
		pairs := `pairs AS MATERIALIZED (SELECT e.*,` + ranked + ` AS ranked,` + ranking + ` AS claim_rank FROM eligible e)`
		available := `available AS MATERIALIZED (SELECT w.* FROM pairs w,clock WHERE w.ranked=0 OR (w.claim_rank>=0 OR w.claim_ranking='[]') AND NOT (` + rankHold + `))`
		return `,` + pairs + `,` + available, nil
	}
	burst := q.at("w.request", "request", "burst")
	burstPrefs := q.at("w.request", "request", "burst", "preferences")
	steps := `steps AS MATERIALIZED (
 SELECT w.team,w.run_id,w.node_id,w.source_generation,w.kind,` + q.index("p") + ` AS step,` + q.number(q.at("p.value", "Hold")) + ` AS hold,COALESCE(` + q.at("p.value", "Prefers") + `,'[]') AS selectors,'' AS target FROM work w CROSS JOIN ` + q.indexed(param, "p") + ` WHERE ` + burst + ` IS NULL AND w.preferences='[]'
 UNION ALL SELECT w.team,w.run_id,w.node_id,w.source_generation,w.kind,0,` + fmt.Sprint(firstPreferenceHold(policy)) + `,w.preferences,'' FROM work w WHERE ` + burst + ` IS NULL AND w.preferences<>'[]'
 UNION ALL SELECT w.team,w.run_id,w.node_id,w.source_generation,w.kind,1,0,'[]','' FROM work w WHERE ` + burst + ` IS NULL AND w.preferences<>'[]'
 UNION ALL SELECT w.team,w.run_id,w.node_id,w.source_generation,w.kind,` + q.index("p") + `,` + q.number(q.at("p.value", "hold")) + `,'[]',` + q.at("p.value", "target") + ` FROM work w CROSS JOIN ` + q.indexed(burstPrefs, "p") + ` WHERE ` + burst + ` IS NOT NULL
 )`
	stepLabels := q.anyAlternative("s.selectors", func(alt string) string { return q.contains(q.at("e.facts", "labels"), alt) })
	stepMatch := `(s.target='' AND (s.selectors='[]' OR ` + stepLabels + `)) OR (s.target='cloud' AND ` + q.number(q.at("e.facts", "cloud")) + `=1) OR (s.target='here' AND ` + q.at("e.facts", "here_run") + `=e.run_id) OR (s.target='machines' AND ` + q.number(q.at("e.facts", "personal")) + `=1) OR ` + q.contains(q.at("e.facts", "labels"), "s.target")
	pairs := `pairs AS MATERIALIZED (SELECT e.*,` + ranked + ` AS ranked,` + ranking + ` AS claim_rank,COALESCE((SELECT MIN(s.step) FROM steps s WHERE ` + same("s", "e") + ` AND (` + stepMatch + `)),(SELECT COUNT(*) FROM steps s WHERE ` + same("s", "e") + `)) AS preference_rank FROM eligible e)`
	prefHold := `EXISTS (SELECT 1 FROM pairs b JOIN steps s ON ` + same("s", "b") + ` AND s.step=b.preference_rank WHERE ` + same("b", "w") + ` AND b.preference_rank<w.preference_rank AND clock.now<w.arrived+(SELECT SUM(s2.hold) FROM steps s2 WHERE ` + same("s2", "w") + ` AND s2.step<=s.step))`
	available := `available AS MATERIALIZED (SELECT w.* FROM pairs w,clock WHERE (w.ranked=0 OR (w.claim_rank>=0 OR w.claim_ranking='[]') AND NOT (` + rankHold + `)) AND (w.ranked=1 OR w.kind<>'node' OR NOT (` + prefHold + `)))`
	return `,` + steps + `,` + pairs + `,` + available, nil
}

func firstPreferenceHold(policy ClaimPlacement) int64 {
	if len(policy.Preferences) == 0 {
		return 0
	}
	return int64(policy.Preferences[0].Hold)
}
