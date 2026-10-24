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
	FreeSlots      int                        `json:"free_slots,omitempty"`
	Current        int                        `json:"current,omitempty"`
	ObservedAt     int64                      `json:"observed_at,omitempty"`
	Prefix         string                     `json:"prefix,omitempty"`
	Team           string                     `json:"team,omitempty"`
	Name           string                     `json:"name,omitempty"`
	HereRun        string                     `json:"here_run,omitempty"`
	BindRun        string                     `json:"bind_run,omitempty"`
	BindKind       string                     `json:"bind_kind,omitempty"`
	BindHolder     string                     `json:"bind_holder,omitempty"`
	BindGeneration int64                      `json:"bind_generation,omitempty"`
	ClaimBy        int64                      `json:"claim_by,omitempty"`
	Here           int                        `json:"here,omitempty"`
	Local          int                        `json:"local,omitempty"`
	Named          int                        `json:"named,omitempty"`
	Cloud          int                        `json:"cloud,omitempty"`
	Launcher       int                        `json:"launcher,omitempty"`
	Metered        int                        `json:"metered,omitempty"`
	Personal       int                        `json:"personal,omitempty"`
	Account        string                     `json:"account,omitempty"`
	Tier           string                     `json:"tier,omitempty"`
	Trust          string                     `json:"trust,omitempty"`
	Actions        int                        `json:"actions,omitempty"`
	Kinds          []string                   `json:"kinds,omitempty"`
	Labels         []string                   `json:"labels,omitempty"`
	PlacementSizes []claimPlacementSize       `json:"placement_sizes,omitempty"`
	Pools          []string                   `json:"pools,omitempty"`
	Scopes         [][]claimRepositoryPattern `json:"scopes,omitempty"`
	Accept         []claimRepositoryPattern   `json:"accept,omitempty"`
	Filter         int                        `json:"filter,omitempty"`
	Pushes         []capability.PushScope     `json:"pushes,omitempty"`
	Excluded       []string                   `json:"excluded,omitempty"`
}

type claimPlacementSize struct {
	Class  int64    `json:"class"`
	Labels []string `json:"labels"`
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
	f := claimFacts{FreeSlots: 1}
	if demand.FreeSlots != nil {
		f.FreeSlots = max(0, *demand.FreeSlots)
	}
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
	f.Local = boolInt(leaf.Subject.Kind == capability.SubjectLocal)
	// safety: indexed head rechecks name a candidate without changing a queued poll into foreground intent.
	f.Named = boolInt(demand.Target != nil)
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
	profile.Labels, profile.OS, profile.Arch = slices.Clone(demand.Labels), demand.OS, demand.Arch
	if f.Cloud == 1 {
		for _, size := range match.CloudSizes() {
			profile.Labels = append(profile.Labels, size.Label)
		}
	}
	f.Labels = slices.DeleteFunc(match.AuthenticatedLabels(profile), func(label string) bool {
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
			f.PlacementSizes = append(f.PlacementSizes, claimPlacementSize{Class: size.CreditClass, Labels: placementSizeLabels(f.Labels, size)})
		}
	}
	f.Pools = slices.Clone(profile.ReservedPools)
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
		rows, err := tx.queryContext(ctx, liveTeamsSQL+` ORDER BY name`)
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
	if _, err := tx.execContext(ctx, `UPDATE claim_awards SET ordinal=ordinal+1 WHERE singleton=1`); err != nil {
		return err
	}
	_, err := tx.execContext(ctx, `UPDATE teams SET claim_award_ordinal=(SELECT ordinal FROM claim_awards WHERE singleton=1) WHERE name=?`, team)
	return err
}

type claimSQL struct {
	placementFree   bool
	preferencesFree bool
	heads           []claimHead
	holdProof       bool
	projected       bool
	target          *UnitRef
	team            string
	ordinal         *int64
	route           bool
	candidate       bool
	search          bool
	context         string
	requirement     string
	runScope        string
	facts           []claimFacts
	teams           []claimTeamFacts
	rates           CreditRateTable
	kinds           []capability.UnitKind
	now             int64
	dialect         Dialect
	args            []any
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
	return `jsonb(CAST(` + v + ` AS TEXT))`
}

func (q *claimSQL) at(v string, path ...string) string {
	var value string
	if q.dialect == DialectPostgres {
		value = `((` + v + `)::jsonb #>> '{` + strings.Join(path, ",") + `}')`
	} else {
		value = `json_extract(` + v + `,'$.` + strings.Join(path, ".") + `')`
	}
	if strings.HasSuffix(v, ".facts") && len(path) == 1 {
		switch path[0] {
		case "prefix", "team", "name", "here_run", "bind_run", "bind_kind", "bind_holder", "account", "tier", "trust":
			return `COALESCE(` + value + `,'')`
		}
	}
	return value
}

func (q *claimSQL) array(v, alias string) string {
	if q.dialect == DialectPostgres {
		return `jsonb_array_elements(COALESCE((` + v + `)::jsonb,'[]'::jsonb)) AS ` + alias + `(value)`
	}
	return `jsonb_each(COALESCE(` + v + `,'[]')) AS ` + alias
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
	pinned := q.anyAlternative(selector, func(alt string) string {
		return `substr(` + alt + `,1,5)='pool=' OR substr(` + alt + `,1,5)='name=' OR substr(` + alt + `,1,6)='class='`
	})
	position := "instr"
	if q.dialect == DialectPostgres {
		position = "strpos"
	}
	// Escaped JSON can conceal the required '=' until alternatives are decoded.
	return `CASE WHEN ` + selector + ` IS NULL THEN FALSE WHEN ` + position + `(` + selector + `,'=')=0 AND ` + position + `(` + selector + `,'\')=0 THEN FALSE ELSE ` + pinned + ` END`
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

func (q *claimSQL) pushScopeMatches(provenance, push, repo string) string {
	p := func(name string) string { return q.at(provenance, name) }
	b := func(name string) string { return q.at(push, name) }
	return `COALESCE((` + p("trust") + ` IN ('actions','verified','verified-default') AND ` + b("workflow_run_id") + `<>'' AND ` + b("repo_key") + `=` + repo + ` AND ` + b("repo_key") + `='gh:'||CAST(` + p("repository_id") + ` AS TEXT) AND ` + b("ref") + `=` + p("ref") + ` AND ` + b("sha") + `=` + p("sha") + `),FALSE)`
}

func writerPrincipalSQL(writer, kind string) string {
	prefix := kind + "/"
	return `CASE WHEN substr(` + writer + `,1,` + fmt.Sprint(len(prefix)) + `)='` + prefix + `' THEN replace(replace(replace(substr(` + writer + `,` + fmt.Sprint(len(prefix)+1) + `),'%2F','/'),'%3A',':'),'%25','%') ELSE '' END`
}

// Both placement explanations and awards use these SQL rules so their trust
// decisions cannot diverge when a new provenance or pool mode is added.
func (q *claimSQL) poolTrust(team, pools, trust, launcher, provenance string) string {
	reserved := `EXISTS (SELECT 1 FROM secrets s WHERE s.team=` + team + ` AND ` + q.contains(pools, "s.only_on") + `)`
	hasPools := `EXISTS (SELECT 1 FROM ` + q.array(pools, "pool") + `)`
	return `((NOT (` + reserved + ` OR (` + trust + `='verified-default' AND ` + hasPools + `)) OR ` + launcher + `=1) AND (` + trust + `='any' OR (` + trust + `='' AND NOT (` + reserved + `)) OR ` + q.at(provenance, "trust") + ` IN ('verified-default','schedule')))`
}

func (q *claimSQL) predicate() string {
	f := q.fact
	n := q.flag
	w := func(path ...string) string { return q.at("w.request", append([]string{"request"}, path...)...) }
	horizon := fmt.Sprint(int64(q.policy.Liveness))
	if q.policy.Liveness <= 0 {
		horizon = `CASE WHEN ` + w("burst") + ` IS NOT NULL OR (` + `w.run_anywhere=1` + `) THEN ` + fmt.Sprint(int64(time.Minute)) + ` ELSE 0 END`
	}
	live := `(` + n("current") + `=1 OR ((` + horizon + `)>0 AND ` + n("observed_at") + `>=clock.now-(` + horizon + `)))`
	labels := q.requirementsMatchSQL("w.team", "w.requirements", func(alt string) string { return q.contains(f("labels"), alt) })
	reservation := `(` + f("pools") + ` IS NULL OR NOT EXISTS (SELECT 1 FROM ` + q.array(f("pools"), "pool") + `) OR EXISTS (SELECT 1 FROM ` + q.array(f("pools"), "pool") + ` WHERE ` + q.contains("w.allowed_pools", q.text("pool.value")) + ` AND ` + q.anyAlternative("w.selectors", func(alt string) string { return alt + `='pool='||` + q.text("pool.value") }) + `))`
	scopes := `NOT EXISTS (SELECT 1 FROM ` + q.array(f("scopes"), "scope") + ` WHERE NOT EXISTS (SELECT 1 FROM ` + q.array("scope.value", "pattern") + ` WHERE (` + q.at("pattern.value", "key") + `=w.repo_key AND w.repo_key<>'') OR (` + q.at("pattern.value", "like") + `<>'' AND w.repository LIKE ` + q.at("pattern.value", "like") + ` ESCAPE '\' AND length(w.repository)-length(replace(w.repository,'/',''))=` + q.number(q.at("pattern.value", "slashes")) + `)))`
	accepted := `(` + n("filter") + `=0 OR (w.kind IN ('node','eval') AND w.repo_display='' AND COALESCE(` + w("git", "repository", "display") + `,'')='') OR EXISTS (SELECT 1 FROM ` + q.array(f("accept"), "pattern") + ` WHERE ` + q.at("pattern.value", "like") + `<>'' AND w.repository LIKE ` + q.at("pattern.value", "like") + ` ESCAPE '\' AND length(w.repository)-length(replace(w.repository,'/',''))=` + q.number(q.at("pattern.value", "slashes")) + `))`
	trust := q.poolTrust("w.team", f("pools"), f("trust"), n("launcher"), q.at("w.request", "provenance"))
	bind := `(` + f("bind_run") + `='' OR (w.run_id=` + f("bind_run") + ` AND (` + n("here") + `=0 OR (w.kind='trigger' AND EXISTS (SELECT 1 FROM triggers bt WHERE bt.team=w.team AND bt.id=w.run_id AND (bt.generation<>0 OR ` + n("claim_by") + `>clock.now))))))`
	pushes := `NOT EXISTS (SELECT 1 FROM ` + q.array(f("pushes"), "push") + ` WHERE NOT (` + q.pushScopeMatches(q.at("w.request", "provenance"), "push.value", "w.repo_key") + `))`
	personal := `(` + n("personal") + `=0 OR (` + f("account") + `<>'' AND ` + f("account") + `=w.requester AND NOT EXISTS (SELECT 1 FROM ancestors a WHERE a.team=w.team AND a.child=w.run_id AND a.origin IN ('github_app','schedule'))))`
	runnerKind := `COALESCE(` + w("options", "runner", "value", "kind") + `,'')`
	runnerValue := `COALESCE(` + w("options", "runner", "value", "value") + `,'')`
	runnerLabels := `json_array(` + runnerValue + `)`
	if q.dialect == DialectPostgres {
		runnerLabels = `jsonb_build_array(` + runnerValue + `)::text`
	}
	runnerLabelMatch := q.terms(runnerLabels, func(alt string) string { return q.contains(f("labels"), alt) })
	runner := `(` + runnerKind + `<>'here' OR ` + f("here_run") + `=w.run_id OR (` + w("burst") + ` IS NOT NULL AND w.kind='node')) AND (` + runnerKind + `<>'name' OR (` + runnerValue + `<>'' AND ` + f("name") + `=` + runnerValue + `)) AND (` + runnerKind + `<>'github-actions' OR w.kind NOT IN ('trigger','plan') OR ` + n("actions") + `=1) AND (` + runnerKind + `<>'label' OR (` + runnerValue + `<>'' AND ` + runnerLabelMatch + `))`
	burst := `(` + w("burst") + ` IS NULL OR (` + f("here_run") + `=w.run_id AND w.kind<>'node') OR EXISTS (SELECT 1 FROM ` + q.array(w("burst", "preferences"), "step") + ` WHERE (` + q.at("step.value", "target") + `='cloud' AND ` + n("cloud") + `=1) OR (` + q.at("step.value", "target") + `='here' AND ` + f("here_run") + `=w.run_id) OR (` + q.at("step.value", "target") + `='machines' AND ` + n("personal") + `=1) OR ((` + n("personal") + `=1 OR ` + f("here_run") + `=w.run_id) AND ` + q.contains(f("labels"), q.at("step.value", "target")) + `)))`
	foreground := `w.kind<>'trigger' OR COALESCE(` + q.at("w.request", "run_id") + `,'')<>w.run_id OR COALESCE(CAST(` + w("local_foreground") + ` AS TEXT),'false') NOT IN ('true','1') OR EXISTS (SELECT 1 FROM triggers ft WHERE ft.team=w.team AND ft.id=w.run_id AND ft.generation>0)`
	foreground += ` OR (` + n("current") + `=1 AND ` + n("named") + `=1 AND ` + n("local") + `=1 AND ` + f("name") + `=w.created_principal)`
	guards := `(w.kind NOT IN ('node','eval') OR ` + `CAST(` + q.at("w.spec", "resolved", "guards", "passed") + ` AS TEXT) IN ('true','1') OR (COALESCE(` + q.at("w.spec", "resolved", "guards", "when") + `,'[]')='[]' AND COALESCE(` + q.at("w.spec", "resolved", "guards", "unless") + `,'[]')='[]' AND COALESCE(` + q.at("w.spec", "resolved", "guards", "reason") + `,'')=''))`
	// Facts that no claimant carries need no correlated relation in the query plan.
	any := func(test func(claimFacts) bool) bool { return slices.ContainsFunc(q.facts, test) }
	if !any(func(f claimFacts) bool { return len(f.Scopes) > 0 }) {
		scopes = "1=1"
	}
	if !any(func(f claimFacts) bool { return f.Filter != 0 }) {
		accepted = "1=1"
	}
	if !any(func(f claimFacts) bool { return f.Trust != "any" || len(f.Pools) > 0 }) {
		trust = "1=1"
	}
	if !any(func(f claimFacts) bool { return f.BindRun != "" }) {
		bind = "1=1"
	}
	if !any(func(f claimFacts) bool { return len(f.Pushes) > 0 }) {
		pushes = "1=1"
	}
	if !any(func(f claimFacts) bool { return f.Personal != 0 }) {
		personal = "1=1"
	}
	if !any(func(f claimFacts) bool { return len(f.Pools) > 0 }) {
		reservation = "1=1"
	}
	excluded := `NOT (` + q.contains(f("excluded"), "w.team") + `)`
	if !any(func(f claimFacts) bool { return len(f.Excluded) > 0 }) {
		excluded = "1=1"
	}
	return strings.Join([]string{
		labels, live, q.contains(f("kinds"), "w.kind"), excluded, bind, pushes, scopes, accepted, trust, personal, runner, burst, guards, reservation, `(` + foreground + `)`,
	}, " AND ")
}

func (q *claimSQL) anyAlternative(selector string, test func(string) string) string {
	term := q.text("term.value")
	return `EXISTS (SELECT 1 FROM ` + q.array(selector, "term") + ` WHERE EXISTS (SELECT 1 FROM ` + q.alternatives(term) + ` WHERE alt<>'' AND (` + test("alt") + `)))`
}

type claimTeamFacts struct {
	Team      string `json:"team"`
	Ordinal   int64  `json:"-"`
	Available int64  `json:"available"`
	Balance   int64  `json:"balance"`
	Held      int    `json:"held"`
}

func claimTeamFactsTx(ctx context.Context, tx *storeTx, scopeTeam string) ([]claimTeamFacts, error) {
	query := `SELECT name,claim_award_ordinal FROM teams WHERE name IN (` + liveTeamsSQL + `)`
	var args []any
	if scopeTeam != "" {
		query += ` AND name=?`
		args = append(args, scopeTeam)
	}
	rows, err := tx.queryContext(ctx, query+` ORDER BY claim_award_ordinal,name`, args...)
	if err != nil {
		return nil, err
	}
	var teams []claimTeamFacts
	for rows.Next() {
		var team claimTeamFacts
		if err := rows.Scan(&team.Team, &team.Ordinal); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		teams = append(teams, team)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range teams {
		team := Team(teams[i].Team)
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
		teams[i].Available = standing.CloudVCPU - active
		teams[i].Balance = balance
		teams[i].Held = boolInt(standing.HoldCause != "" || standing.Tier == "Held")
	}
	return teams, nil
}

func (o *Operator) claimObserversTx(ctx context.Context, tx *storeTx, current claimFacts, scopeTeam string) ([]claimFacts, error) {
	policy, _ := claimPlacementFromContext(ctx)
	horizon := policy.Liveness
	if horizon <= 0 {
		horizon = time.Minute
	}
	now := tx.now(ctx)
	query := `SELECT prefix,claim_observation_json FROM capabilities WHERE claim_observation_json IS NOT NULL AND last_used_at>=? AND prefix<>?`
	args := []any{now.Add(-horizon).UnixNano(), current.Prefix}
	if scopeTeam != "" {
		query += ` AND (team=? OR subject_kind='service')`
		args = append(args, scopeTeam)
	}
	rows, err := tx.queryContext(ctx, query+` ORDER BY prefix`, args...)
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
				queries = append(queries, `SELECT n.team,n.run_id,n.node_id,0 AS source_generation,COALESCE(n.ready_at,clock.now) AS arrived,'node' AS kind,`+q.blob("n.execution_json")+` AS execution,n.claim_requirements AS requirements FROM nodes n,clock WHERE `+q.sourceScope("n", kind))
				continue
			}
			from := `nodes n,clock`
			if q.heads != nil {
				from = `source_heads sh JOIN nodes n ON n.team=sh.team AND n.run_id=sh.run_id AND n.node_id=sh.node_id,clock`
			}
			if q.route {
				from = `nodes n`
			}
			queries = append(queries, `SELECT n.team,n.run_id,n.node_id,0 AS source_generation,n.ready_at AS arrived,'node' AS kind,`+q.blob("n.execution_json")+` AS execution,n.claim_requirements AS requirements FROM `+from+` WHERE `+q.sourceScope("n", kind)+` AND n.status='pending' AND n.ready_at IS NOT NULL AND n.ready_at<=clock.now AND n.claimed_by IS NULL AND n.capability_prefix='' AND n.cancel_requested_at IS NULL AND NOT EXISTS (SELECT 1 FROM runs dr WHERE dr.team=n.team AND dr.id=n.run_id AND dr.claim_wait_ns>0 AND (n.queue_started_at IS NULL OR n.queue_started_at<=clock.now-dr.claim_wait_ns))`)
		case capability.UnitEval:
			from := `eval_units e JOIN nodes n ON n.team=e.team AND n.run_id=e.run_id AND n.node_id=e.source_node_id`
			clockJoin := `,clock`
			if q.heads != nil {
				from = `source_heads sh JOIN eval_units e ON e.team=sh.team AND e.run_id=sh.run_id AND e.source_node_id=sh.node_id AND e.source_generation=sh.source_generation JOIN nodes n ON n.team=e.team AND n.run_id=e.run_id AND n.node_id=e.source_node_id`
			}
			if q.route {
				clockJoin = ``
			}
			if q.route && q.dialect == DialectSQLite {
				// The readiness range must drive the join before pending eval units.
				from = `nodes n CROSS JOIN eval_units e ON n.team=e.team AND n.run_id=e.run_id AND n.node_id=e.source_node_id`
				clockJoin = ``
			}
			from += ` JOIN triggers req ON req.team=e.team AND req.id=e.run_id`
			queries = append(queries, `SELECT e.team,e.run_id,e.source_node_id AS node_id,e.source_generation,`+q.evaluationArrivalSQL()+` AS arrived,'eval' AS kind,NULL AS execution,req.claim_requirements AS requirements FROM `+from+` JOIN leases l ON l.name=e.owner_lease_name`+clockJoin+` WHERE `+q.sourceScope("e", kind)+` AND e.status='pending' AND e.claimed_by IS NULL AND e.capability_prefix='' AND (e.expires_at IS NULL OR e.expires_at>clock.now) AND l.name='controller.owner' AND l.until>clock.now AND n.generation=e.source_generation AND `+evaluationSourceSQL(q, "n"))
		default:
			from := `triggers t JOIN runs tr ON tr.team=t.team AND tr.id=t.id,clock`
			if q.heads != nil {
				from = `source_heads sh JOIN triggers t ON t.team=sh.team AND t.id=sh.run_id JOIN runs tr ON tr.team=t.team AND tr.id=t.id,clock`
			}
			if q.route {
				from = `triggers t JOIN runs tr ON tr.team=t.team AND tr.id=t.id`
			}
			queries = append(queries, `SELECT t.team,t.id AS run_id,'' AS node_id,0 AS source_generation,t.available_at AS arrived,`+q.param(string(kind))+` AS kind,NULL AS execution,t.claim_requirements AS requirements FROM `+from+` WHERE `+q.sourceScope("t", kind)+` AND t.unit_kind=`+q.param(string(kind))+` AND t.status='pending' AND t.claimed_by IS NULL AND t.capability_prefix='' AND t.cancel_requested_at IS NULL AND t.available_at<=clock.now AND (t.expires_at IS NULL OR t.expires_at>clock.now) AND (t.started_at IS NOT NULL OR tr.claim_wait_ns<=0 OR tr.created_at>clock.now-tr.claim_wait_ns)`)
		}
	}
	return strings.Join(queries, ` UNION ALL `)
}

func (q *claimSQL) query(facts []claimFacts, teams []claimTeamFacts, rates CreditRateTable, kinds []capability.UnitKind, demand UnitDemand, now int64) (string, error) {
	q.facts = facts
	q.runScope = ""
	q.target = demand.Target
	q.team = ""
	if len(facts) > 0 && facts[0].Cloud == 0 {
		q.team = facts[0].Team
	} else if len(teams) == 1 {
		q.team = teams[0].Team
	}
	clock := `CAST(` + q.param(now) + ` AS BIGINT)`
	currentCTE := ``
	sqlFacts := facts
	if q.holdProof {
		// Keep current rank separate so proof eligibility contains only observers.
		current := claimFacts{Personal: facts[0].Personal, Name: facts[0].Name, Tier: facts[0].Tier}
		raw, err := q.jsonParam(current)
		if err != nil {
			return "", err
		}
		currentCTE = `current_claimant(facts) AS (SELECT ` + raw + `),`
		sqlFacts = facts[1:]
	}
	factsParam, err := q.jsonParam(sqlFacts)
	if err != nil {
		return "", err
	}
	teamsParam, err := q.jsonParam(teams)
	if err != nil {
		return "", err
	}
	overflowCTE, err := q.overflowFactsSQL()
	if err != nil {
		return "", err
	}
	rateParam, err := q.jsonParam(rates)
	if err != nil {
		return "", err
	}
	headsCTE := ""
	if q.heads != nil {
		heads := make([]struct {
			Team Team
			Unit UnitRef
		}, len(q.heads))
		for i, head := range q.heads {
			heads[i].Team, heads[i].Unit = head.Team, head.Unit
		}
		raw, err := q.jsonParam(heads)
		if err != nil {
			return "", err
		}
		headsCTE = `source_heads AS MATERIALIZED (SELECT ` + q.at("head.value", "Team") + ` AS team,` + q.at("head.value", "Unit", "RunID") + ` AS run_id,` + q.at("head.value", "Unit", "NodeID") + ` AS node_id,` + q.at("head.value", "Unit", "Kind") + ` AS kind,` + q.number(q.at("head.value", "Unit", "SourceGeneration")) + ` AS source_generation FROM ` + q.array(raw, "head") + `),`
	}
	runScopeCTE := ""
	current := facts[0]
	if scope := q.boundRunScope(current, demand.Target); scope != "" {
		q.runScope = `SELECT br.id FROM runs br WHERE ` + scope
	}
	if q.runScope != "" {
		runScopeCTE = `bound_runs AS MATERIALIZED (` + q.runScope + `),`
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
	// Current funding cannot invalidate a hold for an eligible better tier.
	fundingNeeded := slices.ContainsFunc(sqlFacts, func(f claimFacts) bool { return f.Metered == 1 })
	if fundingNeeded {
		var sizes []string
		for ordinal, size := range match.CloudSizes() {
			sizes = append(sizes, `SELECT '`+size.Label+`' AS label,`+fmt.Sprint(size.CreditClass)+` AS cpu_class,`+fmt.Sprint(ordinal)+` AS ordinal`)
		}
		class = `(SELECT size.cpu_class FROM (` + strings.Join(sizes, ` UNION ALL `) + `) size WHERE ` + q.terms("unfunded.selectors", func(alt string) string {
			return `(` + alt + `=size.label OR (NOT (substr(` + alt + `,1,10)='sparkwing-' AND substr(` + alt + `,-3)='cpu') AND ` + q.contains(q.at("unfunded.facts", "labels"), alt) + `))`
		}) + ` ORDER BY size.ordinal LIMIT 1)`
	}
	writer := q.at(request, "provenance", "writer")
	account, machine := writerPrincipalSQL(writer, "account"), writerPrincipalSQL(writer, "machine")
	raw := `SELECT ready.team,ready.run_id,ready.node_id,ready.source_generation,ready.arrived,ready.kind,ready.requirements,r.repo_key,r.repo_display,` + repository + ` AS repository,` + request + ` AS request,` + spec + ` AS spec,` + selectors + ` AS selectors,` + prefers + ` AS preferences,` + q.blob("r.allowed_pools") + ` AS allowed_pools,r.created_principal,tm.claim_ranking,tm.claim_award_ordinal,
 CASE WHEN (` + account + `)<>'' THEN ` + account + ` WHEN (` + machine + `)<>'' THEN (SELECT CASE WHEN COUNT(*)>0 AND COUNT(DISTINCT m.created_by)=1 AND MIN(CASE WHEN COALESCE(CAST(` + q.at(q.blob("m.authority_json"), "origin", "personal") + ` AS TEXT),'false') IN ('true','1') AND m.created_by<>'' THEN 1 ELSE 0 END)=1 THEN MIN(m.created_by) ELSE '' END FROM capabilities m WHERE m.team=r.team AND m.subject_id=(` + machine + `) AND m.subject_kind='machine' AND m.bind_kind='none' AND m.parent_id IS NULL) ELSE '' END AS requester
 FROM ready JOIN runs r ON r.team=ready.team AND r.id=ready.run_id JOIN teams tm ON tm.name=r.team,clock
 WHERE r.team IN (` + liveTeamsSQL + `) AND r.status IN ('pending','running') AND r.cancel_requested_at IS NULL AND r.intake_mode='execute' AND NOT EXISTS (SELECT 1 FROM triggers deadline WHERE deadline.team=r.team AND deadline.id=r.id AND deadline.expires_at<=clock.now)`
	ancestorOrigin := q.at(q.blob("ar.request_json"), "request", "origin")
	ancestorParent := `CASE WHEN ` + ancestorOrigin + `='retry' THEN ar.retry_of WHEN ` + ancestorOrigin + `='child' THEN COALESCE(ar.parent_run_id,'') ELSE '' END`
	funded := `(` + q.number(q.at("sized.facts", "metered")) + `=0 OR (sized.cpu_class IS NOT NULL AND ((rates.price IS NULL AND ` + q.number(q.at("sized.facts", "current")) + `=1) OR (tf.held=0 AND tf.balance>=rates.price*` + fmt.Sprint(SliceSeconds) + ` AND (` + q.number(q.at("sized.facts", "cloud")) + `=0 OR tf.available>=sized.cpu_class)))))`

	overflowAllowed := q.overflowAllowedSQL("sized", q.number(q.at("sized.facts", "cloud")))
	cloudObservers := slices.ContainsFunc(sqlFacts, func(f claimFacts) bool { return f.Cloud != 0 })
	if q.holdProof && !cloudObservers {
		overflowAllowed = `1=1`
	}
	claimantMaterialized := "MATERIALIZED"
	claimantScopes := `claimant_scopes(team,facts) AS MATERIALIZED (SELECT tf.team,c.facts FROM team_facts tf JOIN claimants c ON c.is_cloud=1 UNION ALL SELECT c.team,c.facts FROM claimants c WHERE c.is_cloud=0)`
	if !cloudObservers {
		claimantMaterialized = "NOT MATERIALIZED"
		claimantScopes = `claimant_scopes AS NOT MATERIALIZED (SELECT team,facts FROM claimants)`
	}
	claimants := `SELECT value,` + q.at("fact.value", "team") + `,` + q.number(q.at("fact.value", "cloud")) + ` FROM ` + q.array(factsParam, "fact")
	eligibility := q.predicate()
	workProjection := "w.*"
	if q.holdProof {
		workProjection = "w.team,w.run_id,w.node_id,w.source_generation,w.arrived,w.kind,w.claim_ranking,w.run_anywhere,w.pinned"
		if fundingNeeded {
			workProjection += ",w.selectors"
		}
		if cloudObservers || slices.ContainsFunc(q.facts, func(f claimFacts) bool { return f.Personal != 0 }) {
			workProjection += ",w.request"
		}
		if cloudObservers {
			workProjection += ",w.created_principal"
		}
	}
	materialized := "MATERIALIZED"
	// The proof consumes eligibility once without a size projection to retain.
	if q.holdProof && !fundingNeeded {
		materialized = "NOT MATERIALIZED"
	}
	fundingCTEs := `,sized AS MATERIALIZED (SELECT unfunded.*,` + class + ` AS cpu_class FROM unfunded),eligible AS MATERIALIZED (SELECT sized.*,rates.price FROM sized LEFT JOIN rates ON rates.cpu_class=sized.cpu_class JOIN team_facts tf ON tf.team=sized.team WHERE ` + funded + ` AND ` + overflowAllowed + `)`
	if !fundingNeeded {
		fundingCTEs = `,eligible AS ` + materialized + ` (SELECT sized.*,CAST(NULL AS BIGINT) AS cpu_class,CAST(NULL AS BIGINT) AS price FROM unfunded sized WHERE ` + overflowAllowed + `)`
	}
	ctes := `WITH RECURSIVE clock(now) AS (SELECT ` + clock + `),` + currentCTE + `claimants(facts,team,is_cloud) AS ` + claimantMaterialized + ` (` + claimants + `),
 team_facts AS MATERIALIZED (SELECT ` + q.at("tf.value", "team") + ` AS team,` + q.number(q.at("tf.value", "available")) + ` AS available,` + q.number(q.at("tf.value", "balance")) + ` AS balance,` + q.number(q.at("tf.value", "held")) + ` AS held FROM ` + q.array(teamsParam, "tf") + `),
 ` + claimantScopes + `,
 ` + overflowCTE + `,
 rates AS MATERIALIZED (SELECT ` + q.number(q.at("rate.value", "cores")) + ` AS cpu_class,` + q.number(q.at("rate.value", "micro_per_second")) + ` AS price FROM ` + q.array(rateParam, "rate") + `),
 ` + runScopeCTE + headsCTE + `source(team,run_id,node_id,source_generation,arrived,kind,execution,requirements) AS MATERIALIZED (` + ready + `),ready AS NOT MATERIALIZED (SELECT * FROM source ` + readyFilter + `),work_input AS MATERIALIZED (` + raw + `),work AS MATERIALIZED (SELECT base.*,CASE WHEN ` + q.anywhere("base.request") + ` THEN 1 ELSE 0 END AS run_anywhere,CASE WHEN ` + q.pinned("base.selectors") + ` THEN 1 ELSE 0 END AS pinned FROM work_input base),
 ancestors(team,child,id,origin,parent) AS (SELECT ar.team,ar.id,ar.id,` + ancestorOrigin + `,` + ancestorParent + ` FROM runs ar JOIN work aw ON aw.team=ar.team AND aw.run_id=ar.id UNION SELECT a.team,a.child,ar.id,` + ancestorOrigin + `,` + ancestorParent + ` FROM ancestors a JOIN runs ar ON ar.team=a.team AND ar.id=a.parent),
 unfunded AS ` + materialized + ` (SELECT ` + workProjection + `,c.facts FROM work w JOIN claimant_scopes c ON c.team=w.team,clock WHERE ` + eligibility + `)` + fundingCTEs
	placement, err := q.placementCTEs(q.policy)
	if err != nil {
		return "", err
	}
	ctes += placement
	if q.holdProof {
		currentRank := q.claimRankSQL("w", "current.facts")
		return ctes + ` SELECT DISTINCT w.team,w.run_id,w.node_id,w.source_generation,w.kind FROM admissible w CROSS JOIN current_claimant current,clock WHERE w.ranked=1 AND clock.now<w.arrived+` + fmt.Sprint(int64(limits.RankingWindow)) + ` AND w.claim_rank>=0 AND w.claim_rank<` + currentRank, nil
	}
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
	scopeTeam := ""
	if current.Cloud == 0 {
		scopeTeam = current.Team
	}
	q.policy, _ = claimPlacementFromContext(ctx)
	if demand.Target != nil {
		team, found, err := tx.operator.runOwnerTx(ctx, tx, string(demand.Target.RunID))
		if err != nil {
			return err
		}
		if found {
			if current.Cloud == 1 || current.Team == string(team) {
				scopeTeam = string(team)
			}
			q.placementFree, q.preferencesFree, err = claimPlacementFreeTx(ctx, tx, team, *demand.Target, q.policy)
			if err != nil {
				return err
			}
		}
	}
	facts := []claimFacts{current}
	if !q.placementFree {
		facts, err = tx.operator.claimObserversTx(ctx, tx, current, scopeTeam)
		if err != nil {
			return err
		}
	}
	teams, err := claimTeamFactsTx(ctx, tx, scopeTeam)
	if err != nil {
		return err
	}
	rates, err := creditRateTableTx(ctx, tx)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(facts, func(f claimFacts) bool { return f.Cloud != 0 }) {
		q.overflow, err = tx.operator.claimOverflowAccountsTx(ctx, tx, scopeTeam, current, demand.Target)
		if err == nil && (demand.Target != nil || current.BindRun != "" || len(current.Pushes) > 0) {
			q.overflow, err = claimOverflowUsageTx(ctx, tx, q.overflow)
		}
		if err != nil {
			return err
		}
	}
	q.dialect = tx.dialect
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
	if demand.Target == nil {
		return searchClaimTx(ctx, tx, demand, &q)
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

func (q *claimSQL) claimRankSQL(alias, facts string) string {
	tier := q.at(facts, "tier")
	if slices.ContainsFunc(q.facts, func(f claimFacts) bool { return f.Personal != 0 }) {
		tier = `CASE WHEN ` + q.number(q.at(facts, "personal")) + `=1 AND ` + q.at(facts, "name") + `=COALESCE(` + q.at(alias+".request", "request", "origin_machine") + `,'') THEN 'here' ELSE ` + tier + ` END`
	}
	defaults := `CASE ` + tier
	for rank, name := range defaultClaimRanking {
		defaults += ` WHEN ` + q.param(name) + ` THEN ` + fmt.Sprint(rank)
	}
	defaults += ` ELSE -1 END`
	return `CASE WHEN ` + alias + `.claim_ranking='' THEN ` + defaults + ` ELSE COALESCE((SELECT ` + q.index("tier") + ` FROM ` + q.indexed(alias+".claim_ranking", "tier") + ` WHERE ` + q.text("tier.value") + `=` + tier + ` LIMIT 1),-1) END`
}

func (q *claimSQL) placementCTEs(policy ClaimPlacement) (string, error) {
	// Owner execution demands offer a slot separately from queue polls.
	admissible := `admissible AS NOT MATERIALIZED (SELECT * FROM pairs e WHERE ` + q.number(q.at("e.facts", "free_slots")) + `>0 AND (e.ranked=0 OR e.claim_rank>=0 OR e.claim_ranking='[]'))`
	if q.placementFree {
		return `,pairs AS MATERIALIZED (SELECT e.*,0 AS ranked,0 AS claim_rank FROM eligible e),` + admissible + `,available AS MATERIALIZED (SELECT * FROM admissible)`, nil
	}
	var param string
	if !q.preferencesFree {
		var err error
		param, err = q.jsonParam(policy.Preferences)
		if err != nil {
			return "", err
		}
	}
	ranked := `CASE WHEN e.run_anywhere=1 AND e.pinned=0 THEN 1 ELSE 0 END`
	ranking := q.claimRankSQL("e", "e.facts")
	same := func(a, b string) string { return unitIdentitySQL(a, b) }
	rankHold := q.rankingHold()
	if q.preferencesFree {
		pairs := `pairs AS MATERIALIZED (SELECT e.*,` + ranked + ` AS ranked,` + ranking + ` AS claim_rank FROM eligible e)`
		available := `available AS MATERIALIZED (SELECT w.* FROM admissible w,clock WHERE w.ranked=0 OR NOT (` + rankHold + `))`
		return `,` + pairs + `,` + admissible + `,` + available, nil
	}
	burst := q.at("w.request", "request", "burst")
	burstPrefs := q.at("w.request", "request", "burst", "preferences")
	steps := `steps AS MATERIALIZED (
 SELECT w.team,w.run_id,w.node_id,w.source_generation,w.kind,` + q.index("p") + ` AS step,` + q.number(q.at("p.value", "Hold")) + ` AS hold,COALESCE(` + q.at("p.value", "Prefers") + `,'[]') AS selectors,'' AS target FROM work w CROSS JOIN ` + q.indexed(param, "p") + ` WHERE ` + burst + ` IS NULL AND w.preferences='[]'
 UNION ALL SELECT w.team,w.run_id,w.node_id,w.source_generation,w.kind,0,` + fmt.Sprint(firstPreferenceHold(policy)) + `,w.preferences,'' FROM work w WHERE ` + burst + ` IS NULL AND w.preferences<>'[]'
 UNION ALL SELECT w.team,w.run_id,w.node_id,w.source_generation,w.kind,1,0,'[]','' FROM work w WHERE ` + burst + ` IS NULL AND w.preferences<>'[]'
 UNION ALL SELECT w.team,w.run_id,w.node_id,w.source_generation,w.kind,` + q.index("p") + `,` + q.number(q.at("p.value", "hold")) + `,'[]',` + q.at("p.value", "target") + ` FROM work w CROSS JOIN ` + q.indexed(burstPrefs, "p") + ` WHERE ` + burst + ` IS NOT NULL
 )`
	labels := `CASE WHEN ` + q.number(q.at("e.facts", "cloud")) + `=1 THEN (SELECT ` + q.at("ps.value", "labels") + ` FROM ` + q.array(q.at("e.facts", "placement_sizes"), "ps") + ` WHERE ` + q.number(q.at("ps.value", "class")) + `=e.cpu_class) ELSE ` + q.at("e.facts", "labels") + ` END`
	stepLabels := q.anyAlternative("s.selectors", func(alt string) string { return q.contains(labels, alt) })
	stepMatch := `(s.target='' AND (s.selectors='[]' OR ` + stepLabels + `)) OR (s.target='cloud' AND ` + q.number(q.at("e.facts", "cloud")) + `=1) OR (s.target='here' AND ` + q.at("e.facts", "here_run") + `=e.run_id) OR (s.target='machines' AND ` + q.number(q.at("e.facts", "personal")) + `=1) OR ` + q.contains(labels, "s.target")
	pairs := `pairs AS MATERIALIZED (SELECT e.*,` + ranked + ` AS ranked,` + ranking + ` AS claim_rank,COALESCE((SELECT MIN(s.step) FROM steps s WHERE ` + same("s", "e") + ` AND (` + stepMatch + `)),(SELECT COUNT(*) FROM steps s WHERE ` + same("s", "e") + `)) AS preference_rank FROM eligible e)`
	prefHold := `EXISTS (SELECT 1 FROM admissible b JOIN steps s ON ` + same("s", "b") + ` AND s.step=b.preference_rank WHERE ` + same("b", "w") + ` AND b.preference_rank<w.preference_rank AND clock.now<w.arrived+(SELECT SUM(s2.hold) FROM steps s2 WHERE ` + same("s2", "w") + ` AND s2.step<=s.step))`
	available := `available AS MATERIALIZED (SELECT w.* FROM admissible w,clock WHERE (w.ranked=0 OR NOT (` + rankHold + `)) AND (w.kind<>'node' OR NOT (` + prefHold + `)))`
	return `,` + steps + `,` + pairs + `,` + admissible + `,` + available, nil
}

func firstPreferenceHold(policy ClaimPlacement) int64 {
	if len(policy.Preferences) == 0 {
		return 0
	}
	return int64(policy.Preferences[0].Hold)
}

func (q *claimSQL) rankingHold() string {
	better := `EXISTS (SELECT 1 FROM admissible b WHERE ` + unitIdentitySQL("b", "w") + ` AND b.claim_rank>=0 AND b.claim_rank<w.claim_rank)`
	return `w.claim_rank>0 AND clock.now<w.arrived+` + fmt.Sprint(int64(limits.RankingWindow)) + ` AND ` + better
}

func (q *claimSQL) overflowAllowedSQL(alias, cloud string) string {
	return `(` + q.at(alias+".request", "request", "burst") + ` IS NULL OR ` + q.overflowBudgetAllowedSQL(alias+`.team`, alias+`.created_principal`, q.at(alias+".request", "request", "burst", "overflow_minutes"), cloud) + `)`
}

func (q *claimSQL) overflowBudgetAllowedSQL(team, account, minutes, cloud string) string {
	minimum := q.overflowMinimumMinutesSQL("of.used_seconds")
	return `(` + cloud + `=0 OR NOT EXISTS (SELECT 1 FROM overflow_facts of WHERE of.team=` + team + ` AND of.account=` + account + ` AND ((of.monthly_minutes IS NOT NULL AND ` + q.number("of.monthly_minutes") + `<` + minimum + `) OR (` + minutes + ` IS NOT NULL AND ` + q.number(minutes) + `<` + minimum + `))))`
}

func (q *claimSQL) overflowMinimumMinutesSQL(used string) string {
	return `((` + q.number(used) + `+` + fmt.Sprint(SliceSeconds+59) + `)/60)`
}

func (q *claimSQL) overflowFactsSQL() (string, error) {
	input, err := q.jsonParam(q.overflow)
	if err != nil {
		return "", err
	}
	return `overflow_facts AS MATERIALIZED (SELECT ` + q.at("of.value", "team") + ` AS team,` + q.at("of.value", "account") + ` AS account,` + q.number(q.at("of.value", "used_seconds")) + ` AS used_seconds,` + q.at("of.value", "monthly_minutes") + ` AS monthly_minutes FROM ` + q.array(input, "of") + `)`, nil
}
