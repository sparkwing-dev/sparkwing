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
	"(*Operator).setPaymentModeTx":               "startup cutover checks all teams for open test work and restarts every storage watermark before serving",
	"(*Operator).claimObserversTx":               "claim placement discovers live claimant observations across teams, then checks every pair against authenticated scope",
	"(*Operator).claimOverflowAccountsTx":        "deployment claims gather each live team's overflow policy before the shared eligibility predicate filters pairs",
	"(*Operator).claimTargetTeamTx":              "only the Cloud launcher resolves a globally named run; other claimants query their authenticated team before kind or trust diagnostics",
	"(*Operator).cancelRestoredSchedulesTx":      "offline restore cancels every tenant scheduled run through its scoped lifecycle owner before preparation is recorded",
	"(*Operator).AcknowledgeRestore":             "offline restore acknowledgement advances every tenant schedule past the recovery window before schedules resume",
	"(*Operator).installAuditProtection":         "baseline audit collision protection checks globally unique event IDs across teams",
	"(*Operator).installRunTombstones":           "baseline tombstone trigger writes OLD.team for each deleted run, including bulk retention and purge",
	"(*Operator).readAuthorityNodeTx":            "a global capability prefix or session hash identifies authority before its authenticated team and immutable membership epoch can be resolved",
	"(*Operator).recordSessionMembershipsTx":     "sign-in discovers all current memberships of its authenticated session before recording each through a tenant handle",
	"(*Operator).lookupSessionTx":                "a global session digest identifies the one session being validated and renewed before its team is known",
	"(*Operator).setupAuthorityRefsTx":           "bootstrap completion discovers the deployment seed and its complete descendant graph across teams before locking them",
	"(*Operator).acquireRowLock":                 "the complete ordered lock set contains verified global authority hashes and installation IDs whose tenant is discovered by the mutation",
	"(*Operator).discoverAccountTeams":           "account session creation discovers every current membership before acquiring the complete ordered team and account lock set",
	"(*Operator).discoverAccountSessionLocks":    "account mutations discover every session and its selected team before locking account ancestry",
	"(*Operator).discoverDeletionAuthorityLocks": "account and team deletion discover globally selected credentials and their teams before revoking exact rows",
	"(*Operator).discoverDeletionAccountLocks":   "account deletion discovers all memberships that account mutations can update before locking them",
	"(*Operator).discoverAccountDeletionLocks":   "deleting one deployment account discovers all affected memberships and ledger teams before taking the complete lock set",

	"(*Operator).ListRuns":                  "the operator lists runs across all tenants",
	"(*Operator).getLatestRun":              "the operator locates the latest pipeline run across tenants",
	"(*Operator).ComputeUsage":              "deployment capacity sums metered awards from every tenant",
	"(*Operator).ListWaitingNodes":          "deployment attention samples every tenant queue before updating individual tenant nodes",
	"(*Operator).MarkBlobTombstones":        "leased garbage collection compares every tenant blob with the complete live reference graph",
	"(*Operator).SealAbandonedLogStreams":   "leased log maintenance discovers abandoned streams across every tenant",
	"(*Operator).dueRunEffects":             "the completion worker discovers due effects across tenants before dispatching each tenant run",
	"(*Operator).expiredUnits":              "the leased reaper discovers expired execution units across tenants before expiring each exact unit",
	"(*Operator).launcherKeysTx":            "launcher observations name global unit identities whose stored fence and capability ancestry prove ownership before any award is returned",
	"(*Operator).launcherPrefixesTx":        "launcher reconciliation discovers every tenant award before validating each exact award",
	"(*Operator).lockMaintenanceCapacityTx": "the leased concurrency pass locks capacity entries across tenants before maintaining each tenant key",
	"(*Operator).retainedStorageTeams":      "storage billing discovers every live tenant holding retained records or blobs",
	"(*Operator).deleteRetentionRunsTx":     "leased retention discovers finished runs across tenants before deleting exact tenant run keys",
	"(*Operator).deleteRetentionEffectsTx":  "leased retention discovers settled effects across tenants before deleting exact tenant effect keys",
	"(*Operator).SearchTeams":               "the operator console searches every tenant and its owners by deployment search text",
	"(*Operator).nodeRunActiveSQL":          "the shared predicate correlates a node with its own run; its enclosing statement supplies the tenant scope",
	"(*Operator).paidGrantForPaymentTx":     "a verified provider payment identity resolves its paid grant before its tenant is known",
	"(*Operator).accountHasOtherTeamTx":     "an account must retain another membership when deleting its current tenant; account identity spans tenants",

	"(*Operator).abandonExpiredBlobUploads": "leased maintenance selects expired uploads across teams, then abandons each through its scoped transaction owner",
	"(*Operator).ExpireSourceUploads":       "leased maintenance selects expired source tickets across teams, then expires each exact tenant ticket in its own transaction",
	"(*Operator).DeleteExpiredBlobStaging":  "leased maintenance selects settled blob tickets across teams, then marks each exact tenant ticket after its staging delete",
	"(*Operator).EnsureBootstrapCapability": "the deployment bootstrap seed is a teamless capability, and startup compares a mounted seed with it by prefix",
	"(*Operator).launcherAwardPrefixTx":     "launcher reconciliation resolves the exact public unit fence across tenants before rechecking the launcher ancestry and reservation",

	"(*Operator).recordAudit":                    "the shared event writer accepts an explicit nullable team because deployment account events have no tenant",
	"(*Operator).githubInstallationConnectionTx": "a global installation ID resolves its tenant before tenant-scoped mutation or delivery scheduling",
	"(*Operator).revokeSelectedCapabilitiesTx":   "account and team deletion supply a checked selector, then revoke each selected credential through its full authority ancestry",

	"(*Operator).hasDeploymentAdministratorTx":  "deployment readiness and the bootstrap gate select only credentials whose team is NULL, then validate their complete live ancestry",
	"(*Operator).LookupCapability":              "a presentation digest identifies the credential before its tenant is known",
	"(*Operator).bindCompletedPageTx":           "a verified page completion resolves its team from the session correlation committed before payment",
	"(*Operator).RecordsPaymentParent":          "a verified provider event names provider identities before its tenant is known",
	"(*Operator).OpenStandingRequests":          "the operator console queue lists every team's open standing requests",
	"(*Operator).loadCapabilityRecordTx":        "globally unique prefixes resolve the complete authority envelope before its tenant and ancestry are checked",
	"(*Operator).persistCapabilityTx":           "indexed columns, including nullable team, are serialized from the complete authority envelope in one INSERT",
	"(*Operator).deleteRetentionCapabilitiesTx": "the elected deployment pass removes ended credentials across teams while preserving cross-team ancestry",

	"(*Operator).refundableBillingSecondsTx": "the deployment credit sampler totals unsettled awards across teams from their immutable billing records",
	"(*Operator).CollectBlobTombstones":      "the elected blob collector visits expired tombstones across teams and deletes each exact tenant digest",
	"(*Operator).maintainRetentionTx":        "the elected maintenance pass counts retained pins and records across all teams",
	"(*Operator).deleteRetentionRecordsTx":   "the elected maintenance pass selects expired records across teams and deletes exact tenant record keys",

	"(*Operator).ReconcileControllerRuns":   "the elected controller enumerates active controller-owned runs across teams, then settles each exact tenant/run under the same lease fence",
	"(*Operator).authenticationAuthorityTx": "a globally unique presentation digest identifies the session before its tenant and current membership are known",
	"(*Operator).revokeAuthorityTx":         "the authorized revocation path names its exact capability or session by global identity",
	"(*Operator).lockUnitsTx":               "a unit's globally unique run id yields the team its owner, run and job locks are scoped by",
	"(*Operator).loadMatchingRunTx":         "reads the run's team by global id, then refuses a foreign claimant unless it holds deployment launcher authority",
	"(*Operator).loadAwardRowTx":            "awards derive team from the globally identified unit and check it against the authenticated claimant before issuing authority",
	"(*Operator).deleteRetentionMetricsTx":  "the elected deployment maintenance pass selects expired metrics across teams and deletes each exact team/run/job/time identity",
	"(*Operator).deleteRetentionIdentityTx": "the elected deployment maintenance pass expires every team's spent identity rows while preserving session ancestry",
	"(*Operator).closeRefundedQueueTx":      "a provider refund names a payment identity, and a queued refund it covers is settled for whichever team queued it",
	"(*Operator).reconcileSetupStateTx":     "a verified setup event resolves its payer from the setup correlation committed before payment",
	"(*Operator).discoverPaymentTeamsTx":    "payment identity discovers the affected team set before taking team and ledger locks",
	"(*Operator).reducePaymentStateTx":      "a verified refill payment names its provider payment, which resolves the one refill that recorded it",
	"(*Operator).reconcilePaymentStateTx":   "the payment owner resolves the payer by mode and provider payment identity before settling its normalized state",
	"(*Operator).paymentTeamTx":             "a provider payment identity is resolved to the checkout or refill that took it before its team is known",
	"(*Operator).CreditLedgerTotals":        "the deployment credit sampler totals every team's ledger in one snapshot",
	"(*Operator).DueRefills":                "the elected refill worker lists teams with refill on and open refills, then opens or resumes each under that team's lock",
	"(*Operator).applyCheckoutPaymentTx":    "a verified event resolves its committed checkout by configured mode and provider session, then rejects payment identities already used by any other checkout",

	"(*Operator).DueCardRefunds":            "the payment worker lists every team's queued refunds in one pass",
	"(*Operator).runOwnerTx":                "asks which team owns an id, so an answer scoped to the asker is no answer",
	"(*Operator).SampleSealedSecrets":       "samples envelopes from any team to prove the configured key opens them before the startup reseal writes anything",
	"(*Operator).unlinkAccountIdentityTx":   "ends one account's sessions in every team, because the sign-in it removed could have opened any of them",
	"(*Operator).DeleteSession":             "ends the one session whose globally unique digest the caller presented, before its team is known",
	"(*Operator).mintCSRFKey":               "a new signing key cannot sign any team's live sessions, so key and sessions rotate together",
	"(*Operator).resolveSignInTx":           "counts one account's memberships in every team, because a sign-in decides whether that human has any team at all",
	"(*Operator).approveOneTx":              "counts one account's memberships in every team, because an admission decides whether that human needs a personal space",
	"(*Operator).settleActiveTeamTx":        "picks the account's first team from all of its memberships when its sticky team is gone",
	"(*Operator).AccountMemberships":        "lists the teams one account belongs to, which is a question across teams by definition",
	"(*Operator).OpenInvitationsForAccount": "lists the invitations addressed to the account's verified emails from every team that sent one",
	"(*Operator).AcceptAccountInvitation":   "finds the invitation by its global id to lock its team before rechecking the account and committing the scoped acceptance",
	"(*Operator).acceptAccountInvitationTx": "finds an invitation by its id before the team is known; the accepting write then names that team",
	"(*Operator).deleteAccountTx": "removes one account from every team it belongs to and relabels the rows it " +
		"left in each, because an account is the deployment's and reaches teams only through memberships",

	"(*Operator).relabelPrincipalTx": "replaces a deleted account's name in every team's rows, because the account " +
		"belonged to the deployment and acted in each of its teams",
	"(*Operator).grantCreatorTeamsTx": "lists every team holding grants a deleted account created, to lock each team's ledger " +
		"before relabelling them",
	"(*Operator).ClaimInvitationEmail": "counts the invitation emails one address received from every team, " +
		"because the daily cap protects the inbox, not the team",
	"(*Operator).GitHubAppInstallationTeam": "a webhook delivery and a repository's installation name no team, " +
		"so the installation's binding is how either finds the team it belongs to",
	"(*Operator).FreeSlots":              "the free tier is bounded by how many teams hold a slot, so it counts every team's",
	"(*Operator).freeSlotOpenTx":         "a reservation asks whether any slot is left, so it counts every team's",
	"(*Operator).releaseIdleFreeSlotsTx": "leased retention inspects every team's slot and releases only its exact team after thirty empty days without bytes or runs",
	"(*Operator).disputeHoldTx": "asks which payment and team a dispute's hold names, so a hold or reversal naming it for " +
		"another team is refused; a dispute id is unique across teams",

	"(*Operator).PruneEgressUsage": "expires every team's egress reports under the maintenance lease",
	"(*Operator).ownedTeamsTx": "lists the teams one account belongs to as owner, with each team's own owner and member counts, so the " +
		"account deletion can tell sole ownership from shared",

	"(*Operator).RunTeam": "asks which team owns a run id a sweep found across teams, so the sweep acts on it through that " +
		"team's handle; an answer scoped to the asker is no answer",
	"(*Operator).CountNodesByQueueState": "an operational gauge of the deployment's node queue across every team",
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

// Unbound SQL belongs to a reviewed Operator operation. Scoped methods and
// package functions cannot acquire an exemption by adding an allowlist entry.
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
		// The recursive graph has one shared owner; its bound output, rather
		// than each intermediate CTE row, scopes this tenant-only existence check.
		if stmt.key == "blobReachableTx" && strings.HasPrefix(stmt.text, reachableBlobsSQL) {
			output := strings.TrimPrefix(stmt.text, reachableBlobsSQL)
			if statementCarriesTeam(output) && len(unscopedSubqueries(output)) == 0 {
				continue
			}
		}
		if _, fragment := reviewedClaimSQLFragments[stmt.key]; fragment {
			seen[stmt.key] = true
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
	for key, why := range reviewedClaimSQLFragments {
		if !strings.HasPrefix(key, "(*claimSQL).") || why == "" || !seen[key] {
			t.Errorf("stale or unexplained claim SQL fragment %s", key)
		}
	}
	for key, why := range reviewedUnscopedSQL {
		if !strings.HasPrefix(key, "(*Operator).") {
			t.Errorf("unscoped SQL belongs to *Operator, cannot exempt %q", key)
		}
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
		{"join alone", `SELECT n.id FROM nodes n JOIN runs r ON n.team=r.team`, false},
		{"excluded team", `SELECT id FROM runs WHERE team<>?`, false},
		{"nonempty team", `SELECT id FROM runs WHERE team != ''`, false},
		{"literal team", `SELECT id FROM runs WHERE team = 'someone'`, false},
		{"unbound team IN", `SELECT id FROM runs WHERE team IN (SELECT team FROM nodes)`, false},
		{"bound team IN", `SELECT id FROM runs WHERE team IN (?)`, true},
		{"unscoped select", `SELECT id FROM runs WHERE id = ?`, false},
		{"qualified predicate", `UPDATE nodes SET x = 1 WHERE nodes.team = ? AND run_id = ?`, true},
		{"comment is not a filter", `SELECT id FROM nodes -- WHERE team = ?`, false},
		{"literal is not a filter", `SELECT 'WHERE team = ?' FROM nodes`, false},
		{"assignment is not a filter", `UPDATE nodes SET team = ? WHERE id = ?`, false},
		{"sort is not a filter", `SELECT id FROM nodes ORDER BY team = ?`, false},
		{"empty tenant wildcard", `SELECT id FROM credit_grants WHERE (? = '' OR team = ?) AND reference = ?`, false},
		{"unbound alternative", `SELECT id FROM nodes WHERE team = ? OR status = ?`, false},
		{"bound alternatives", `SELECT id FROM nodes WHERE team = ? OR team = ?`, true},
		{"comment between tokens", `SELECT id FROM nodes WHERE team /* scope */ = ?`, true},
		{"insert every row bound", `INSERT INTO secrets(team,name) VALUES (?, ?), (?, ?)`, true},
		{"insert one row unbound", `INSERT INTO secrets(team,name) VALUES (?, ?), ('foreign', ?)`, false},
		{"insert select bound source", `INSERT INTO secrets(team,name) SELECT team,name FROM secrets WHERE team=?`, true},
		{"insert select unbound source", `INSERT INTO secrets(team,name) SELECT team,name FROM secrets`, false},
		{"uncorrelated EXISTS does not bind outer tenant", `SELECT id FROM nodes WHERE EXISTS (SELECT 1 FROM runs WHERE team=?)`, false},
		{"non-team IN does not bind outer tenant", `SELECT id FROM nodes WHERE status IN (SELECT status FROM runs WHERE team=?)`, false},
		{"projection is not a filter", `SELECT team = ? FROM nodes`, false},
		{"insert literal tenant", `INSERT INTO secrets(team,name) VALUES ('foreign', ?)`, false},
		{"conflict cannot bind insert tenant", `INSERT INTO secrets(team,name) VALUES ('foreign', ?) ON CONFLICT(team,name) DO UPDATE SET v=1`, false},
		{"insert with key", `INSERT INTO secrets (team, name, pipeline) VALUES (?,?,?)`, true},
		{"insert without key", `INSERT INTO secrets (name, pipeline) VALUES (?,?)`, false},
		{"conflict target without team", `INSERT INTO secrets (team, name) VALUES (?,?) ON CONFLICT (name) DO UPDATE SET v = 1`, false},
		{"conflict target with team", `INSERT INTO secrets (team, name) VALUES (?,?) ON CONFLICT (team, name) DO UPDATE SET v = 1`, true},
		{"conflict guard with team", `INSERT INTO secrets (team, name) VALUES (?,?) ON CONFLICT (name) DO UPDATE SET v = 1 WHERE secrets.team = excluded.team`, true},
		{"postgres placeholder", `SELECT id FROM runs WHERE team = $1`, true},
		{"operator table", `SELECT value FROM sparkwing_meta WHERE key = ?`, true},
		{"unscoped subquery under a scoped statement", `INSERT INTO nodes (team, run_id, x) VALUES (?, ?, (SELECT x FROM runs WHERE id = ?))`, false},
		{"bound correlated subquery", `SELECT id FROM nodes WHERE team = ? AND EXISTS (SELECT 1 FROM runs r WHERE r.team=nodes.team AND r.id=nodes.run_id)`, true},
		{"shadowed correlation", `SELECT n.id FROM nodes n WHERE n.team=? AND EXISTS (SELECT 1 FROM runs n WHERE n.team=n.team)`, false},
		{"unbound correlated subquery", `SELECT id FROM nodes WHERE EXISTS (SELECT 1 FROM runs r WHERE r.team=nodes.team AND r.id=nodes.run_id)`, false},
		{"scoped subquery", `INSERT INTO nodes (team, run_id, x) VALUES (?, ?, (SELECT x FROM runs WHERE team = ? AND id = ?))`, true},
		{"unscoped exists", `SELECT id FROM nodes WHERE team = ? AND EXISTS (SELECT 1 FROM runs r WHERE r.id = nodes.run_id)`, false},
		{"unscoped nested subquery", `SELECT id FROM nodes WHERE team = ? AND run_id IN (SELECT id FROM runs WHERE team = ? AND retry_of IN (SELECT id FROM runs WHERE id = ?))`, false},
		{"subquery deriving the team", `INSERT INTO nodes (team, run_id) VALUES ((SELECT team FROM runs WHERE id = ?), ?)`, false},
		{"predicate only in a select-list subquery", `SELECT (SELECT pipeline FROM runs WHERE runs.team = nodes.team AND runs.id = nodes.run_id) FROM nodes WHERE run_id = ?`, false},
		{"outer bound with scoped IN", `SELECT run_id FROM approvals WHERE team=? AND run_id IN (SELECT id FROM runs WHERE team=?)`, true},
		{"outer bound with scoped EXISTS", `DELETE FROM nodes WHERE team=? AND EXISTS (SELECT 1 FROM runs r WHERE r.team=nodes.team AND r.id=nodes.run_id)`, true},
		{"outer scoped IN requires its own bound team", `SELECT run_id FROM approvals WHERE resolved_at IS NULL AND run_id IN (SELECT id FROM runs WHERE team = ?)`, false},
		{"outer scoped EXISTS requires its own bound team", `DELETE FROM nodes WHERE EXISTS (SELECT 1 FROM runs r WHERE r.team = ? AND r.id = nodes.run_id)`, false},
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
		if (slices.Contains(tenantTables, name) || slices.Contains([]string{"audit_events", "capabilities", "sessions"}, name)) && !slices.Contains(out, name) {
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
	return scopeBound(scopeTokens(sql), nil)
}

// safety: an outer predicate says nothing about a subquery reading another tenant table by a bare id, and
// a predicate inside a subquery says nothing about the outer rows, so the statement and each parenthesized
// SELECT are judged on their own text. A team derived from an unbound identity still crosses tenants.
func unscopedSubqueries(sql string) []string {
	return unscopedSQLQueries(sql, nil, false)
}

var tableAliasRe = regexp.MustCompile(`(?is)\b(?:FROM|JOIN|UPDATE|INTO)\s+([a-z_][a-z0-9_]*)(?:\s+(?:AS\s+)?([a-z_][a-z0-9_]*))?`)

// Correlation inherits scope only from an enclosing query with a bound team.
func unscopedSQLQueries(sql string, parents []string, check bool) []string {
	parts := splitSubqueries(sql)
	local := queryTeamReferences(parts.outer)
	parents = slices.DeleteFunc(slices.Clone(parents), func(ref string) bool { return slices.Contains(local, ref) })
	bound := scopeBound(scopeTokens(parts.outer), parents)
	var out []string
	if check && len(tenantTablesTouchedBy(parts.outer)) > 0 && !bound {
		out = append(out, parts.outer)
	}
	refs := slices.Clone(parents)
	if bound {
		refs = append(refs, local...)
	}
	for _, sub := range parts.subs {
		out = append(out, unscopedSQLQueries(sub, refs, true)...)
	}
	return out
}

func queryTeamReferences(sql string) []string {
	var refs []string
	for _, m := range tableAliasRe.FindAllStringSubmatch(sql, -1) {
		if m[2] != "" && !slices.Contains([]string{"where", "join", "left", "right", "inner", "on", "set", "order", "group", "union"}, strings.ToLower(m[2])) {
			refs = append(refs, strings.ToLower(m[2])+".team")
		} else {
			refs = append(refs, strings.ToLower(m[1])+".team")
		}
	}
	return refs
}

// Every outer query must bind its own tenant. A subquery cannot supply that
// predicate, regardless of its projection, correlation or position.
func outerCarriesTeam(sql string) bool {
	outer := splitSubqueries(sql).outer
	return len(tenantTablesTouchedBy(outer)) == 0 || statementCarriesTeam(outer)
}

var subqueryStartRe = regexp.MustCompile(`(?is)^\s*(?:SELECT|WITH)\b`)

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
	outer := []byte(sql)
	var out splitSQL
	for _, sp := range spans {
		nested := slices.ContainsFunc(spans, func(o span) bool { return o.from < sp.from && o.to > sp.to })
		if nested {
			continue
		}
		text := sql[sp.from:sp.to]
		out.subs = append(out.subs, text)
		fill := byte(' ')
		for j := sp.from; j < sp.to; j++ {
			outer[j] = fill
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
