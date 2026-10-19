package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// safety: every entry carries its reason and the guard refuses an empty
// one, because an exemption without a reason is a silenced failure.
var reviewedUnscopedSQL = map[string]string{
	"(*Operator).abandonExpiredBlobUploads": "leased maintenance selects expired uploads across teams, then abandons each through its scoped transaction owner",
	"(*Operator).ExpireSourceUploads":       "leased maintenance selects expired source tickets across teams, then expires each exact tenant ticket in its own transaction",
	"(*Operator).DeleteExpiredBlobStaging":  "leased maintenance selects settled blob tickets across teams, then marks each exact tenant ticket after its staging delete",
	"revokeSetupAuthorityTx":                "first-administrator setup revokes the global seed and its complete capability/session descendant graph in one transaction",
	"launcherAwardPrefixTx":                 "launcher reconciliation resolves the exact public unit fence across tenants before rechecking the launcher ancestry and reservation",

	"RecordBusinessEvent":                        "the shared event writer accepts an explicit nullable team because deployment account events have no tenant",
	"(*Console).ConsoleUnbindGitHubInstallation": "the console administrator resolves the installation's tenant by global installation ID, then removes only that tenant's binding",
	"revokeSelectedCapabilitiesTx":               "account and team deletion supply a checked selector, then revoke each selected credential through its full authority ancestry",

	"readPaymentCorrelationTx":      "verified provider mode and object resolve the payer from the persisted correlation before its tenant is known",
	"hasDeploymentAdministratorTx":  "deployment readiness and the bootstrap gate select only credentials whose team is NULL, then validate their complete live ancestry",
	"(*Store).LookupCapability":     "a presentation digest identifies the credential before its tenant is known",
	"loadCapabilityRecordTx":        "globally unique prefixes resolve the complete authority envelope before its tenant and ancestry are checked",
	"(*Operator).SetRunnerMetered":  "a console-authorized deployment mutation resolves one enrolled root by its globally unique public prefix, validates its complete ancestry, and updates only that root's tenant row",
	"persistCapabilityTx":           "indexed columns, including nullable team, are serialized from the complete authority envelope in one INSERT",
	"deleteRetentionCapabilitiesTx": "the elected deployment pass removes ended credentials across teams while preserving cross-team ancestry",

	"refundableBillingSecondsTx":        "the deployment credit sampler totals unsettled awards across teams from their immutable billing records",
	"(*Operator).CollectBlobTombstones": "the elected blob collector visits expired tombstones across teams and deletes each exact tenant digest",
	"maintainRetentionTx":               "the elected maintenance pass counts retained pins and records across all teams",
	"deleteRetentionRecordsTx":          "the elected maintenance pass selects expired records across teams and deletes exact tenant record keys",

	"(*Operator).ReconcileControllerRuns": "the elected controller enumerates active controller-owned runs across teams, then settles each exact tenant/run under the same lease fence",
	"readAuthorityTx":                     "a globally unique capability prefix or session hash identifies authority before its team and membership are resolved",
	"authenticationAuthorityTx":           "a globally unique presentation digest identifies the session before its tenant and current membership are known",
	"revokeAuthorityTx":                   "the authorized revocation path names its exact capability or session by global identity",
	"advanceAuthGenerationsTx":            "membership and revocation changes invalidate exact ancestor session hashes, including ancestors outside the affected team",
	"lockAuthorityTx":                     "globally unique capability prefixes and session hashes are locked before authority resolution reads their teams",
	"lockUnitsTx":                         "a unit's globally unique run id yields the team its owner, run and job locks are scoped by",
	"claimUnitTx":                         "the matcher checks claimant team or deployment launcher authority before the claim reads the target run's team by global id",
	"loadMatchingRunTx":                   "reads the run's team by global id, then refuses a foreign claimant unless it holds deployment launcher authority",
	"loadAwardRowTx":                      "awards derive team from the globally identified unit and check it against the authenticated claimant before issuing authority",
	"deleteRetentionMetricsTx":            "the elected deployment maintenance pass selects expired metrics across teams and deletes each exact team/run/job/time identity",
	"deleteRetentionIdentityTx":           "the elected deployment maintenance pass expires every team's spent identity rows while preserving session ancestry",
	"paidGrantForPaymentTx":               "provider payment identity is resolved before the payer is known; the configured mode and persisted checkout select the grant",
	"paymentWarningTeamTx":                "a provider warning names a payment identity; its persisted grant or card attempt supplies the team",
	"(*Store).settlePaymentFacts":         "a verified event resolves its committed checkout by configured mode and provider session, then rejects payment identities already used by any other checkout",

	"(*Operator).DueCardCharges": "the payment worker expires every team's stale pay-now attempts in one pass " +
		"under the ledger lock",
	"startDueAttemptsTx": "the payment worker starts the due attempts of every team's open charges; " +
		"each row it touches is named by its global id",
	"settleCardPaymentTx": "a payment names its charge by global id, and the charge row it reads " +
		"is checked against the team the payment names",
	"failCardAttemptTx":          "a decline names its attempt by global id; the team is read from that row",
	"recordAttemptIntentTx":      "the worker binds a payment to the attempt it names by global id",
	"(*Operator).DueCardRefunds": "the payment worker lists every team's queued refunds in one pass",
	"globalRunsPerHourRefusal":   "the global hourly cap counts runs from every team",
	"runOwnerTx":                 "asks which team owns an id, so an answer scoped to the asker is no answer",
	"(*Store).listRuns": "the team predicate comes from runFilterWhere at run time; " +
		"a scope with no team and no all-teams flag is refused there",
	"(*Store).countRuns": "shares listRuns' predicate builder and its refusal",
	"(*Store).getLatestRun": "the team predicate comes from its scope at run time; " +
		"a scope with no team and no all-teams flag is refused there",
	"(*Store).SampleSealedSecrets":     "samples envelopes from any team to prove the configured key opens them before the startup reseal writes anything",
	"unlinkAccountIdentityTx":          "ends one account's sessions in every team, because the sign-in it removed could have opened any of them",
	"(*Store).lookupSession":           "resolves the globally unique digest of the presented session before its team is known, renews only that session, and reads the token it names by a prefix that is unique across teams",
	"(*Store).DeleteSession":           "ends the one session whose globally unique digest the caller presented, before its team is known",
	"(*Store).mintCSRFKey":             "a new signing key cannot sign any team's live sessions, so key and sessions rotate together",
	"resolveSignInTx":                  "counts one account's memberships in every team, because a sign-in decides whether that human has any team at all",
	"approveOneTx":                     "counts one account's memberships in every team, because an admission decides whether that human needs a personal space",
	"settleActiveTeamTx":               "picks the account's first team from all of its memberships when its sticky team is gone",
	"(*Store).AccountMemberships":      "lists the teams one account belongs to, which is a question across teams by definition",
	"(*Store).OpenInvitationsForEmail": "lists the invitations addressed to one verified email from every team that sent one",
	"(*Store).AcceptAccountInvitation": "finds the invitation by its global id to lock its team before rechecking the account and committing the scoped acceptance",
	"acceptAccountInvitationTx":        "finds an invitation by its id before the team is known; the accepting write then names that team",
	"(*Store).deleteAccountTx": "removes one account from every team it belongs to and relabels the rows it " +
		"left in each, because an account is the deployment's and reaches teams only through memberships",
	"lockOwnedTeamsTx": "lists the teams one account owns across every team, to lock each before the " +
		"account deletion counts their owners",
	"relabelPrincipalTx": "replaces a deleted account's name in every team's rows, because the account " +
		"belonged to the deployment and acted in each of its teams",
	"(*Store).ClaimInvitationEmail": "counts the invitation emails one address received from every team, " +
		"because the daily cap protects the inbox, not the team",
	"(*Operator).GitHubAppInstallationTeam": "a webhook delivery and a repository's installation name no team, " +
		"so the installation's binding is how either finds the team it belongs to",
	"(*Store).FreeSlots": "the free tier is bounded by how many teams hold a slot, so it counts every team's",
	"freeSlotOpenTx":     "a reservation asks whether any slot is left, so it counts every team's",
	"(*Operator).ListCronSchedulesAcrossTeams": "the controller's tick evaluates every team's schedules and resolves and " +
		"launches each one through its own team's handle",
	"disputeHoldTx": "asks which payment and team a dispute's hold names, so a hold or reversal naming it for " +
		"another team is refused; a dispute id is unique across teams",

	"(*Operator).PruneEgressUsage": "expires every team's egress reports under the maintenance lease",
	"ownedTeamsTx": "lists the teams one account belongs to as owner, with each team's own owner and member counts, so the " +
		"account deletion can tell sole ownership from shared",

	"(*Operator).RunTeam": "asks which team owns a run id a sweep found across teams, so the sweep acts on it through that " +
		"team's handle; an answer scoped to the asker is no answer",
	"(*Operator).CountNodesByQueueState": "an operational gauge of the deployment's node queue across every team",
	"(*Store).creditLedgerTotalsWith": "the credit sampler reports the deployment's ledger totals, which sum every team's " +
		"grants, charges and reservations and name none",
	"(*Operator).ListStorageQuotas": "the operator's storage report lists every team's quota, each keyed by its team " +
		"principal",
	"(*Operator).TopStorageTeams": "the operator's storage report ranks every team's monthly bytes, each keyed by its team " +
		"principal",
	"(*Operator).EgressUsageAcrossTeams": "reloads team egress totals into the controller alarm before serving",
}

// safety: matches only after FROM, JOIN, INTO and UPDATE, because a
// table name appearing inside a column name or a comment is not a read
// or a write of that table.
var tenantOwnedTableRe = regexp.MustCompile(`(?is)\b(?:FROM|JOIN|INTO|UPDATE)\s+([a-z_][a-z0-9_]*)`)

// safety: covers every shape this package writes the predicate in: a
// bare column, a qualified one, an IN list, and the excluded.team a
// conflict guard compares against.
var teamPredicateRe = regexp.MustCompile(`(?is)(?:\b(?:[a-z_][a-z0-9_]*\.)?team|\bCOALESCE\(\s*(?:[a-z_][a-z0-9_]*\.)?team\s*,\s*''\s*\))\s*(?:=|IN\b|<>|!=)`)

// safety: reads an INSERT's column list, because a write with no WHERE
// carries its key there and nowhere else.
var insertColumnsRe = regexp.MustCompile(`(?is)INSERT\s+INTO\s+[a-z_][a-z0-9_]*\s*\(([^)]*)\)`)

// safety: splits a conflict clause so a team-aware target and a
// team-aware guard are told apart; either one closes the hole.
var onConflictRe = regexp.MustCompile(`(?is)ON\s+CONFLICT\s*(?:\(([^)]*)\))?`)

// Every statement in this package that touches a tenant-owned table
// carries the tenant key, or its function is exempt. This is the guard
// that replaces the compiler for the length of the port: the ported
// methods still exist byte-identically on *Store and Go satisfies
// interfaces structurally, so nothing but this test refuses an unscoped
// statement.
//
// reviewedUnscopedSQL holds the statements that cross teams on purpose --
// a reaper, a sweep, the dispatch and claim path -- each with its reason.
//
// It is keyed by receiver and function name, so a method moving from
// *Store to *Tenant changes its key and stops being exempt. A function
// that already holds a team cannot be exempt at all; the next test
// enforces that. New unscoped work belongs in reviewedUnscopedSQL with
// its reason.
func TestTenantSQLScope_EveryTenantTableStatementCarriesTheKey(t *testing.T) {
	seen := map[string]bool{}
	for _, stmt := range tenantSQLStatements(t) {
		tables := tenantTablesTouchedBy(stmt.text)
		if len(tables) == 0 {
			continue
		}
		unscoped := unscopedSubqueries(stmt.text)
		if outerCarriesTeam(stmt.text) && len(unscoped) == 0 {
			continue
		}
		if exemptFromScope(stmt.key) {
			seen[stmt.key] = true
			continue
		}
		if len(unscoped) > 0 {
			t.Errorf("%s: statement in %s has a subquery touching %s with no team predicate of its own.\n"+
				"Scope the subquery, or add %q to reviewedUnscopedSQL with the reason it crosses teams.\n\t%s",
				stmt.pos, stmt.key, strings.Join(tenantTablesTouchedBy(unscoped[0]), ", "), stmt.key, collapse(unscoped[0]))
			continue
		}
		t.Errorf("%s: statement in %s touches %s with no team predicate.\n"+
			"Scope it, or add %q to reviewedUnscopedSQL with the reason it crosses teams.\n\t%s",
			stmt.pos, stmt.key, strings.Join(tables, ", "), stmt.key, collapse(stmt.text))
	}
	for key, why := range reviewedUnscopedSQL {
		if strings.TrimSpace(why) == "" {
			t.Errorf("reviewedUnscopedSQL exempts %q with no reason", key)
		}
		if !seen[key] {
			t.Errorf("reviewedUnscopedSQL exempts %q, which no longer has an unscoped statement; delete the entry", key)
		}
	}
}

func exemptFromScope(key string) bool {
	_, ok := reviewedUnscopedSQL[key]
	return ok
}

// An exemption is only honest while the function has no team to use. A
// method on *Tenant, or one handed a Team, holding an unscoped statement
// is the port's failure mode, and an allowlist entry that survived a
// rename would hide exactly that.
func TestTenantSQLScope_AllowlistCannotCoverAScopedFunction(t *testing.T) {
	for _, stmt := range tenantSQLStatements(t) {
		if !exemptFromScope(stmt.key) {
			continue
		}
		if stmt.holdsTeam {
			t.Errorf("%s: %s has a team in scope and is exempted; "+
				"a function that can scope its statement must", stmt.pos, stmt.key)
		}
	}
}

// The guard is worthless if it parses nothing, and a change to how this
// package spells SQL could leave it matching nothing while staying green.
func TestTenantSQLScope_GuardActuallySeesTheStatements(t *testing.T) {
	stmts := tenantSQLStatements(t)
	touching := 0
	for _, stmt := range stmts {
		if len(tenantTablesTouchedBy(stmt.text)) > 0 {
			touching++
		}
	}
	if touching < 400 {
		t.Fatalf("guard found only %d statements touching tenant-owned tables out of %d parsed; "+
			"it is no longer reading this package", touching, len(stmts))
	}
}

// A statement the guard would pass and one it would fail, written here
// so the matcher's behavior is pinned rather than inferred.
func TestTenantSQLScope_MatcherAcceptsAndRefuses(t *testing.T) {
	cases := []struct {
		name  string
		sql   string
		scope bool
	}{
		{"nullable tenant", `SELECT prefix FROM capabilities WHERE COALESCE(team,'')=?`, true},
		{"qualified nullable tenant", `SELECT prefix FROM capabilities c WHERE COALESCE(c.team, '') = ?`, true},
		{"nullable tenant without predicate", `SELECT COALESCE(team,'') FROM capabilities WHERE prefix=?`, false},
		{"scoped select", `SELECT id FROM runs WHERE team = ? AND id = ?`, true},
		{"unscoped select", `SELECT id FROM runs WHERE id = ?`, false},
		{"qualified predicate", `UPDATE nodes SET x = 1 WHERE nodes.team = ? AND run_id = ?`, true},
		{"insert with key", `INSERT INTO secrets (team, name, pipeline) VALUES (?,?,?)`, true},
		{"insert without key", `INSERT INTO secrets (name, pipeline) VALUES (?,?)`, false},
		{"conflict target without team", `INSERT INTO secrets (team, name) VALUES (?,?) ON CONFLICT (name) DO UPDATE SET v = 1`, false},
		{"conflict target with team", `INSERT INTO secrets (team, name) VALUES (?,?) ON CONFLICT (team, name) DO UPDATE SET v = 1`, true},
		{"conflict guard with team", `INSERT INTO secrets (team, name) VALUES (?,?) ON CONFLICT (name) DO UPDATE SET v = 1 WHERE secrets.team = excluded.team`, true},
		{"postgres placeholder", `SELECT id FROM runs WHERE team = $1`, true},
		{"operator table", `SELECT value FROM sparkwing_meta WHERE key = ?`, true},
		{"unscoped subquery under a scoped statement", `INSERT INTO nodes (team, run_id, x) VALUES (?, ?, (SELECT x FROM runs WHERE id = ?))`, false},
		{"scoped subquery", `INSERT INTO nodes (team, run_id, x) VALUES (?, ?, (SELECT x FROM runs WHERE team = ? AND id = ?))`, true},
		{"unscoped exists", `SELECT id FROM nodes WHERE team = ? AND EXISTS (SELECT 1 FROM runs r WHERE r.id = nodes.run_id)`, false},
		{"unscoped nested subquery", `SELECT id FROM nodes WHERE team = ? AND run_id IN (SELECT id FROM runs WHERE team = ? AND retry_of IN (SELECT id FROM runs WHERE id = ?))`, false},
		{"subquery deriving the team", `INSERT INTO nodes (team, run_id) VALUES ((SELECT team FROM runs WHERE id = ?), ?)`, true},
		{"predicate only in a select-list subquery", `SELECT (SELECT pipeline FROM runs WHERE runs.team = nodes.team AND runs.id = nodes.run_id) FROM nodes WHERE run_id = ?`, false},
		{"outer filtered by a scoped IN", `SELECT run_id FROM approvals WHERE resolved_at IS NULL AND run_id IN (SELECT id FROM runs WHERE team = ?)`, true},
		{"outer filtered by a scoped EXISTS", `DELETE FROM nodes WHERE EXISTS (SELECT 1 FROM runs r WHERE r.team = ? AND r.id = nodes.run_id)`, true},
		{"scoped subquery in a SET clause", `UPDATE nodes SET x = (SELECT x FROM runs WHERE team = ? AND id = ?) WHERE run_id = ?`, false},
		{"outer reading no table", `SELECT (SELECT SUM(amount_micro) FROM credit_grants WHERE team = ?), (SELECT SUM(amount_micro) FROM credit_charges WHERE team = ?)`, true},
		{"parenthesis in a literal", `SELECT id FROM runs WHERE team = ? AND note = '(' AND id IN (SELECT run_id FROM nodes WHERE team = ?)`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unscoped := len(tenantTablesTouchedBy(c.sql)) > 0 &&
				(!outerCarriesTeam(c.sql) || len(unscopedSubqueries(c.sql)) > 0)
			if unscoped == c.scope {
				t.Errorf("sql %q: guard says scoped=%v, want %v", c.sql, !unscoped, c.scope)
			}
		})
	}
}

type sqlStatement struct {
	key       string
	pos       string
	text      string
	holdsTeam bool
}

func tenantTablesTouchedBy(sql string) []string {
	var out []string
	for _, m := range tenantOwnedTableRe.FindAllStringSubmatch(sql, -1) {
		name := strings.ToLower(m[1])
		if (slices.Contains(tenantTables, name) || slices.Contains([]string{"business_events", "capabilities", "sessions"}, name)) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// safety: reads one statement's text, so a statement joining two tenant
// tables passes on a single predicate; this is the guard's floor, not a
// proof that every table in the statement is scoped. A subquery is judged
// on its own by unscopedSubqueries.
func statementCarriesTeam(sql string) bool {
	if cols := insertColumnsRe.FindStringSubmatch(sql); cols != nil {
		if !slices.ContainsFunc(strings.Split(cols[1], ","), func(c string) bool {
			return strings.EqualFold(strings.TrimSpace(c), "team")
		}) {
			return false
		}
	}
	if loc := onConflictRe.FindStringSubmatchIndex(sql); loc != nil {
		target := ""
		if loc[2] >= 0 {
			target = sql[loc[2]:loc[3]]
		}
		targetHasTeam := slices.ContainsFunc(strings.Split(target, ","), func(c string) bool {
			return strings.EqualFold(strings.TrimSpace(c), "team")
		})
		return targetHasTeam || teamPredicateRe.MatchString(sql[loc[1]:])
	}
	if insertColumnsRe.MatchString(sql) {
		return true
	}
	return teamPredicateRe.MatchString(sql)
}

// safety: an outer predicate says nothing about a subquery reading another tenant table by a bare id, and
// a predicate inside a subquery says nothing about the outer rows, so the statement and each parenthesized
// SELECT are judged on their own text. A subquery selecting only the team column is exempt: asking which
// team owns an id is how a write takes its row's team.
func unscopedSubqueries(sql string) []string {
	var out []string
	for _, sub := range splitSubqueries(sql).subs {
		if len(tenantTablesTouchedBy(sub)) == 0 || teamPredicateRe.MatchString(sub) || teamDerivationRe.MatchString(sub) {
			continue
		}
		out = append(out, sub)
	}
	return out
}

// safety: a scoped subquery behind IN or EXISTS filters the outer rows to the team, so it scopes the outer
// statement; one in a select list or a SET clause does not, and is cut out before the outer is judged.
func outerCarriesTeam(sql string) bool {
	outer := splitSubqueries(sql).outer
	return len(tenantTablesTouchedBy(outer)) == 0 || statementCarriesTeam(outer)
}

var teamDerivationRe = regexp.MustCompile(`(?is)^\s*SELECT\s+(?:[a-z_][a-z0-9_]*\.)?team\s+FROM\b`)

var subqueryStartRe = regexp.MustCompile(`(?is)^\s*(?:SELECT|WITH)\b`)

var filterBeforeRe = regexp.MustCompile(`(?is)\b(?:IN|EXISTS)\s*$`)

type splitSQL struct {
	outer string
	subs  []string
}

// safety: skips quoted literals, because a parenthesis inside a string is
// not one of the statement's and would misalign every span after it.
func splitSubqueries(sql string) splitSQL {
	type span struct{ from, to int }
	var open []int
	var spans []span
	inQuote := false
	for i := 0; i < len(sql); i++ {
		switch c := sql[i]; {
		case c == '\'':
			inQuote = !inQuote
		case inQuote:
		case c == '(':
			open = append(open, i)
		case c == ')' && len(open) > 0:
			from := open[len(open)-1] + 1
			open = open[:len(open)-1]
			if subqueryStartRe.MatchString(sql[from:i]) {
				spans = append(spans, span{from, i})
			}
		}
	}
	own := func(sp span) string {
		b := []byte(sql[sp.from:sp.to])
		for _, inner := range spans {
			if inner.from > sp.from && inner.to < sp.to {
				for j := inner.from - sp.from; j < inner.to-sp.from; j++ {
					b[j] = ' '
				}
			}
		}
		return string(b)
	}
	outer := []byte(sql)
	var out splitSQL
	for _, sp := range spans {
		text := own(sp)
		out.subs = append(out.subs, text)
		nested := slices.ContainsFunc(spans, func(o span) bool { return o.from < sp.from && o.to > sp.to })
		if nested {
			continue
		}
		fill := byte(' ')
		for j := sp.from; j < sp.to; j++ {
			outer[j] = fill
		}
		if filterBeforeRe.MatchString(sql[:sp.from-1]) && teamPredicateRe.MatchString(text) {
			copy(outer[sp.from:], " team = ? ")
		}
	}
	out.outer = string(outer)
	return out
}

// safety: substitutes the package-level constants holding statement
// fragments, because a statement assembled by concatenation has to be
// judged whole or its appended team predicate is invisible.
func tenantSQLStatements(t *testing.T) []sqlStatement {
	t.Helper()
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = f
	}
	if len(files) == 0 {
		t.Fatal("no source files parsed; the guard would pass vacuously")
	}

	consts := map[string]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if text, ok := flattenSQL(vs.Values[i], nil); ok {
							consts[name.Name] = text
						}
					}
				}
			}
		}
	}

	var out []sqlStatement
	for name, f := range files {
		names := sortedFileNames(f)
		for _, fn := range names {
			ast.Inspect(fn.node, func(n ast.Node) bool {
				expr, ok := n.(ast.Expr)
				if !ok {
					return true
				}
				text, ok := flattenSQL(expr, consts)
				if !ok || !looksLikeSQL(text) {
					return true
				}
				out = append(out, sqlStatement{
					key:       fn.key,
					pos:       name + ":" + strconv.Itoa(fset.Position(expr.Pos()).Line),
					text:      text,
					holdsTeam: fn.holdsTeam,
				})
				return false
			})
		}
	}
	return out
}

type namedFunc struct {
	key       string
	node      ast.Node
	holdsTeam bool
}

func sortedFileNames(f *ast.File) []namedFunc {
	var out []namedFunc
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		out = append(out, namedFunc{key: funcKey(fn), node: fn.Body, holdsTeam: funcHoldsTeam(fn)})
	}
	return out
}

// safety: the receiver is part of the key because moving a method from
// *Store to *Tenant is how a family ports, and a key on the bare name
// would follow it across and keep exempting it.
func funcKey(fn *ast.FuncDecl) string {
	name := fn.Name.Name
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return name
	}
	return "(" + exprString(fn.Recv.List[0].Type) + ")." + name
}

func funcHoldsTeam(fn *ast.FuncDecl) bool {
	if fn.Recv != nil && len(fn.Recv.List) > 0 && strings.Contains(exprString(fn.Recv.List[0].Type), "Tenant") {
		return true
	}
	for _, p := range fn.Type.Params.List {
		if exprString(p.Type) == "Team" {
			return true
		}
	}
	return false
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return "*" + exprString(v.X)
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	default:
		return ""
	}
}

// safety: a fragment it cannot resolve becomes a space, because an
// unresolvable call is not a team predicate and crediting it as one
// would pass an unscoped statement.
func flattenSQL(e ast.Expr, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.Ident:
		if consts == nil {
			return "", false
		}
		s, ok := consts[v.Name]
		return s, ok
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		left, lok := flattenSQL(v.X, consts)
		right, rok := flattenSQL(v.Y, consts)
		if !lok && !rok {
			return "", false
		}
		return left + " " + right, true
	case *ast.ParenExpr:
		return flattenSQL(v.X, consts)
	}
	return "", false
}

var sqlVerbRe = regexp.MustCompile(`(?is)\b(SELECT|INSERT\s+INTO|UPDATE|DELETE\s+FROM)\b`)

func looksLikeSQL(s string) bool { return sqlVerbRe.MatchString(s) }

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }
