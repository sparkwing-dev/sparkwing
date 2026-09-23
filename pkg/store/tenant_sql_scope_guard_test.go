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
	"runOwnerTx": "asks which team owns an id, so an answer scoped to the asker is no answer",
	"(*Store).PaidGrantTeam": "asks which team a payment id was granted to, so a refund that names only " +
		"the payment reverses it in that team; a payment id is unique across teams",
	"(*Store).claimScope": "asks which team a claim credential belongs to, so an answer scoped to " +
		"the asker is no answer; it is the read every other claim predicate is built from",
	"(*Store).readClaimCandidates": "the team predicate comes from claimTeamWhere at run time; " +
		"a scope that is not exactly one team is refused there",
	"(*Store).bumpMismatchedNodes":  "shares the claim scan's runtime predicate and its refusal",
	"executorPrepareCandidateQuery": "shares the claim scan's runtime predicate and its refusal",
	"(*Store).awardScannedNode": "the award carries the same runtime predicate; the read that follows " +
		"it is of the row the award just proved in team",
	"(*Store).ClaimNamedNode": "shares the claim scan's runtime predicate and its refusal",
	"refuseEventOverLimitsTx": "reads one run's event counters for a cap on that run; the run id " +
		"names one team's row and the fence checked before it proves the caller holds that run",
	"backfillRunEventUsageTx": "a v52 migration that counts every run's events onto that run's own row",
	"duplicateTeamGrantReferences": "a v52 migration check that groups every team's grants by team to find " +
		"the rows the per-team reference key would refuse",
	"(*Store).runnerTeams": "asks which team each live runner's credential belongs to, so an " +
		"answer scoped to the asker is no answer; it is how another team's runner is dropped",
	"(*Store).ClaimNextTriggerFor": "shares the claim scan's runtime predicate and its refusal; the " +
		"award and the read after it are of the row the scoped select just locked",
	"(*Store).ClaimSpecificTriggerFor": "shares the claim scan's runtime predicate and its refusal; the " +
		"read after the award is of the row the scoped update just proved in team",
	"(*Store).listRuns": "the team predicate comes from runFilterWhere at run time; " +
		"a scope with no team and no all-teams flag is refused there",
	"(*Store).countRuns": "shares listRuns' predicate builder and its refusal",
	"(*Store).listTriggers": "the team predicate comes from its scope at run time; " +
		"a scope with no team and no all-teams flag is refused there",
	"(*Store).getLatestRun": "the team predicate comes from its scope at run time; " +
		"a scope with no team and no all-teams flag is refused there",
	"(*Store).CountConcurrencyCache":         "an ops gauge of the whole table, which is what the ceiling is set against",
	"(*Store).CountDeadLocalConcurrency":     "the dry run of that repair, and has to count what it would remove",
	"(*Store).ListConcurrencyStates":         "enumerates the deployment's live keys with their teams and reads each through that team's handle",
	"(*Store).PurgeDeadLocalConcurrency":     "repairs local-scope rows left by dead runs across the deployment, and deletes each through its own team",
	"(*Store).reapStaleConcurrencyHolders":   "reaps every team's lease-expired holders, because a sweep that only reaped one team would leave the rest held",
	"(*Store).reapStaleConcurrencyWaiters":   "reaps every team's abandoned waiters, for the same reason as the holder sweep",
	"(*Store).reconcileConcurrencyKeys":      "enumerates every team's queued keys with their teams and promotes each through that team's handle",
	"(*Store).sweepExpiredConcurrencyCache":  "an expiry sweep is the deployment's, and a memo past its TTL answers no team",
	"(*Store).sweepLRUConcurrencyCache":      "the row ceiling is the deployment's, so eviction ranks every team's memos together",
	"(*Store).sweepOrphanedConcurrencyCache": "drops memos whose origin run is gone anywhere on the deployment",
	"rewriteLegacyInheritedHolderMarkers":    "a v27 migration, and the team column arrives in v49",
	"selectSecretsTx":                        "reads every team's secret so one key rotation reseals the whole table, and rewrites each row under its own team",
	"(*Store).secretsNotSealed":              "finds every team's secret still held as plaintext or a pre-team envelope, so the startup reseal binds each to its own team",
	"(*Store).SampleSealedSecrets":           "samples envelopes from any team to prove the configured key opens them before the startup reseal writes anything",
	"(*Store).resolveSignInOnce":             "counts one account's memberships in every team, because a sign-in decides whether that human has any team at all",
	"(*Store).approveOneOnce":                "counts one account's memberships in every team, because an admission decides whether that human needs a personal space",
	"cancelRequeuedCancelledTriggersTx": "finalizes every team's cancelled triggers that lapsed back to the " +
		"queue, because a claim that settled only its own team would leave the rest pending forever",
	"settleTriggerCreditsTx": "asks which team owns a trigger id and whether its claim holds a credit " +
		"reservation; the settlement that follows is scoped to that team",
	"settleActiveTeamTx":               "picks the account's first team from all of its memberships when its sticky team is gone",
	"(*Store).AccountMemberships":      "lists the teams one account belongs to, which is a question across teams by definition",
	"(*Store).OpenInvitationsForEmail": "lists the invitations addressed to one verified email from every team that sent one",
	"(*Store).AcceptInvitation":        "finds an invitation by its id before the team is known; the accepting write then names that team",
	"(*Operator).ListGitHubWebhookBindingsAcrossTeams": "an unauthenticated delivery names no team, so every team's " +
		"binding of the pipeline is a candidate until its secret verifies the signature",
	"(*Store).DeleteAccount": "removes one account from every team it belongs to and relabels the rows it " +
		"left in each, because an account is the deployment's and reaches teams only through memberships",
	"lockOwnedTeamsTx": "lists the teams one account owns across every team, to lock each before the " +
		"account deletion counts their owners",
	"accountPrincipalNamesTx": "lists the principal names of the tokens one account minted in every team, " +
		"which are what the account deletion relabels",
	"relabelPrincipalTx": "replaces a deleted account's name in every team's rows, because the account " +
		"belonged to the deployment and acted in each of its teams",
	"(*Store).ClaimInvitationEmail": "counts the invitation emails one address received from every team, " +
		"because the daily cap protects the inbox, not the team",
	"(*Operator).GitHubAppInstallationTeam": "a webhook delivery and a repository's installation name no team, " +
		"so the installation's binding is how either finds the team it belongs to",
	"(*Operator).usageRuns": "counts every team's runs per week for the operator's usage metrics, " +
		"which report totals across teams and name none",
	"(*Store).expiredRetainedRuns": "the retention window is the deployment's, so the sweep finds every team's " +
		"expired runs and releases each against the team read off its own row",
	"(*Store).PruneSpentIdentity": "deletes every team's invitations, tokens and runner credentials that stopped " +
		"admitting anyone, because a sweep that pruned one team would leave the rest to grow",
	"(*Store).FreeSlots": "the free tier is bounded by how many teams hold a slot, so it counts every team's",
	"(*Operator).ListCronSchedulesAcrossTeams": "the controller's tick evaluates every team's schedules and resolves and " +
		"launches each one through its own team's handle",
	"disputeHoldTx": "asks which payment and team a dispute's hold names, so a hold or reversal naming it for " +
		"another team is refused; a dispute id is unique across teams",
	"(*Store).DisputeTeam": "asks which team a dispute's hold is on, so an operator's release that names only the " +
		"dispute answers with that team; a dispute id is unique across teams",
}

// safety: this list shrinks and never grows; porting a family deletes
// its entries and lowers unportedSQLSize in the same commit, so an entry
// cannot be added without a reviewer seeing the number move.
var unportedSQL = []string{
	"(*Store).AcknowledgeNodeExecutionStart",
	"(*Store).ActiveExecutorActivity",
	"(*Store).AddNodeMetricSample",
	"(*Store).AddNodeUsage",
	"(*Store).AppendEventOnce",
	"(*Store).AppendNodeAnnotation",
	"(*Store).AppendStepAnnotation",
	"(*Store).CacheExcludedCounts",
	"(*Store).CancelPendingTrigger",
	"(*Store).ComputeAlarmState",
	"(*Store).ComputeUsage",
	"(*Store).ConsumeNodeBounce",
	"(*Store).CountActiveRunners",
	"(*Store).CountNodesByQueueState",
	"(*Store).CountPendingNodes",
	"(*Store).CountPendingTriggers",
	"(*Store).CountUsers",
	"(*Store).CreateApproval",
	"(*Store).CreateDebugPause",
	"(*Store).CreateFirstUser",
	"(*Store).CreateSession",
	"(*Store).CreateTokenIfNoneExist",
	"(*Store).CreateUser",
	"(*Store).CreditLedgerTotals",
	"(*Store).DeleteRun",
	"(*Store).DeleteSession",
	"(*Store).DeleteUser",
	"(*Store).ExpireSessions",
	"(*Store).ExtendSession",
	"(*Store).FindSpawnedChildTriggerID",
	"(*Store).FindTriggerByIdempotencyKey",
	"(*Store).FindTriggerByWebhookReplay",
	"(*Store).FinishLapsedClaim",
	"(*Store).FinishNodeExecutionAttempt",
	"(*Store).FinishNodeStep",
	"(*Store).FinishNodeWithReason",
	"(*Store).FinishRun",
	"(*Store).FinishRunAtGeneration",
	"(*Store).FinishRunsIfActive",
	"(*Store).FinishTrigger",
	"(*Store).FinishTriggerAtGeneration",
	"(*Store).GetActiveDebugPause",
	"(*Store).GetApproval",
	"(*Store).GetNode",
	"(*Store).GetNodeDispatch",
	"(*Store).GetRun",
	"(*Store).GetRunAncestorPipelines",
	"(*Store).GetTrigger",
	"(*Store).HeartbeatNodeClaim",
	"(*Store).HeartbeatTrigger",
	"(*Store).ListApprovalsForRun",
	"(*Store).ListCreditCharges",
	"(*Store).ListCreditGrants",
	"(*Store).ListDebugPauses",
	"(*Store).ListEgressUsage",
	"(*Store).ListEventsAfter",
	"(*Store).ListExpiredClaims",
	"(*Store).ListLegacyAgentClaims",
	"(*Store).ListNodeBounces",
	"(*Store).ListNodeDispatches",
	"(*Store).ListNodeExecutionAttempts",
	"(*Store).ListNodeMetrics",
	"(*Store).ListNodeSteps",
	"(*Store).ListNodes",
	"(*Store).ListPendingApprovals",
	"(*Store).ListPendingTriggersForParent",
	"(*Store).ListSpawnedChildrenByRun",
	"(*Store).ListStorageQuotas",
	"(*Store).ListTokens",
	"(*Store).ListUsers",
	"(*Store).LookupSession",
	"(*Store).LookupToken",
	"(*Store).NodeClaimFenceIsLive",
	"(*Store).NodeExecutionAttemptBelongsToLiveClaim",
	"(*Store).NodeExecutionAttemptIsLive",
	"(*Store).NodeSettlement",
	"(*Store).OldestWaitingReadyNodeForPrincipal",
	"(*Store).PendingNodeBounce",
	"(*Store).ClaimedRunFor",
	"(*Store).ClaimedRunsFor",
	"(*Store).PrincipalHoldsNodeClaim",
	"(*Store).PrincipalHoldsPipelineClaim",
	"(*Store).PrincipalHoldsProfileClaim",
	"(*Store).PrincipalHoldsRunClaim",
	"(*Store).PrincipalHoldsTriggerClaim",
	"(*Store).PruneEgressUsage",
	"(*Store).PruneRunsOlderThan",
	"(*Store).ReapExpiredNodeClaims",
	"(*Store).RecordEgressUsage",
	"(*Store).ReleaseClaimAtGeneration",
	"(*Store).ReleaseDebugPause",
	"(*Store).RequestCancel",
	"(*Store).RequestNodeBounce",
	"(*Store).RequeueUnstartedClaim",
	"(*Store).ResetNodeForAutoRetry",
	"(*Store).ResolveApproval",
	"(*Store).RevokeNodeReady",
	"(*Store).RevokeToken",
	"(*Store).RunExceedsWallClock",
	"(*Store).SetNodeArtifactManifest",
	"(*Store).SetNodeArtifactManifestCharged",
	"(*Store).SetNodeStatus",
	"(*Store).SetNodeSummary",
	"(*Store).SetRetriedAs",
	"(*Store).SetStepSummary",
	"(*Store).SetStorageAllowance",
	"(*Store).SetStorageQuota",
	"(*Store).SetTokenMetered",
	"(*Store).SkipNodeStep",
	"(*Store).StartNode",
	"(*Store).StartNodeStep",
	"(*Store).StorageRetainedBytes",
	"(*Store).StorageUsageFor",
	"(*Store).TokenMetered",
	"(*Store).TopStorageTeams",
	"(*Store).TouchNodeHeartbeat",
	"(*Store).TouchRunHeartbeat",
	"(*Store).TriggerClaimFenceIsLive",
	"(*Store).TriggerClaimGeneration",
	"(*Store).TriggerClaimant",
	"(*Store).TriggerExecutionAttemptBelongsToLiveClaim",
	"(*Store).TriggerExecutionAttemptIsLive",
	"(*Store).UpdateNodeActivity",
	"(*Store).UpdateNodeDeps",
	"(*Store).ValidateExecutorClaimReservation",
	"(*Store).VerifyUser",
	"(*Store).WriteNodeDispatch",
	"(*Store).acknowledgeTriggerExecutionStart",
	"(*Store).applyMigrationPostgresTx",
	"(*Store).assertNodeMutationFenceTx",
	"(*Store).awardBestExecutorOffer",
	"(*Store).buildNodeExecutionPolicyTx",
	"(*Store).cancelMeteredNode",
	"(*Store).cascadeOrphanedNodes",
	"(*Store).chargeNodeTx",
	"(*Store).chargeStorageTx",
	"(*Store).claimReadyNodeForExecutorTx",
	"(*Store).createAgentLossRetryTx",
	"(*Store).enforceClaimComputeLimitsTx",
	"(*Store).eventKindPresent",
	"(*Store).executorEligibility",
	"(*Store).executorEligibilityTx",
	"(*Store).expireConflictingExecutorOffersTx",
	"(*Store).expireNodeExecutorOffersTx",
	"(*Store).expirePendingAgentLossRetriesTx",
	"(*Store).expireRunStorage",
	"(*Store).failNodesInRun",
	"(*Store).failStaleQueuedNodes",
	"(*Store).finalizeExecutorClaimRoundAt",
	"(*Store).finishLocalNodeExecutionAttempt",
	"(*Store).finishTriggerExecutionAttempt",
	"(*Store).loadExecutorPreparePlans",
	"(*Store).loadExecutorPrepareProfiles",
	"(*Store).loadExecutorUsage",
	"(*Store).lookupUser",
	"(*Store).markNodeReady",
	"(*Store).mergeAgentLossRetryTx",
	"(*Store).mintCSRFKey",
	"(*Store).orphanedRunsQuery",
	"(*Store).prepareNextExecutorClaim",
	"(*Store).reapExpiredTriggers",
	"(*Store).reapQueueExpiredRuns",
	"(*Store).reapStalePendingRuns",
	"(*Store).reapStaleRunningRuns",
	"(*Store).reapTimedOutApprovals",
	"(*Store).reconcileOrphanedLocalRuns",
	"(*Store).recordExecutorOfferAt",
	"(*Store).recoverExpiredNodeClaims",
	"(*Store).rejectUnattestedExecutorOffer",
	"(*Store).requiredAgentLossRetryNodeSourceTx",
	"(*Store).reserveNodeCreditsTx",
	"(*Store).retainingPrincipals",
	"(*Store).rotateToken",
	"(*Store).schedulingSummaryTx",
	"(*Store).selectTokensByPrefix",
	"(*Store).startLocalNodeExecutionAttempt",
	"(*Store).storageQuotaRow",
	"(*Store).sweepTable",
	"addNodeMetricsRunCascadePostgres",
	"appendEventTx",
	"appendRunAnnotation",
	"applyFleetMigrationPostgres",
	"applyFleetMigrationSQLite",
	"applyMigrationSQLite",
	"backfillAgentLossRetryNodeSourcesTx",
	"backfillRunAnnotationRollup",
	"bridgeLegacyFleetSQLite",
	"claimedExecutorOffer",
	"clearCreditExhaustionAnchorTx",
	"creditExhaustionAnchorTx",
	"duplicateGrantReferences",
	"duplicateTokenPrefixes",
	"enforceNodesPerRunTx",
	"enforceRunsPerHourTx",
	"gatherRunAnnotations",
	"livePrefixesForPrincipal",
	"loadAgentLossRetryNodeSourceTx",
	"loadExecutorUsageTx",
	"lockRunRow",
	"nodeChargeTx",
	"nodeExecutorOfferCountTx",
	"persistAgentLossRetryNodeSourceTx",
	"principalMetered",
	"recentPaidGrantsMicro",
	"rehashSessions",
	"runElapsedSecondsTx",
	"runPrincipalTx",
	"runsPerHourRefusal",
	"scrubSecretInputHashes",
	"selectTokensByPrefixTx",
	"snapshotAgentLossRetryNodesTx",
	"stampCreditExhaustionAnchorTx",
	"storageQuotaForTx",
	"tokenMeteredTx",
	"txLiveRunningRunIDs",
	"txNodeOutcome",
	"validateLegacyFleetShape",
}

// safety: pins the backlog's length so it can only shrink.
const unportedSQLSize = 217

// safety: matches only after FROM, JOIN, INTO and UPDATE, because a
// table name appearing inside a column name or a comment is not a read
// or a write of that table.
var tenantOwnedTableRe = regexp.MustCompile(`(?is)\b(?:FROM|JOIN|INTO|UPDATE)\s+([a-z_][a-z0-9_]*)`)

// safety: covers every shape this package writes the predicate in: a
// bare column, a qualified one, an IN list, and the excluded.team a
// conflict guard compares against.
var teamPredicateRe = regexp.MustCompile(`(?is)\b(?:[a-z_][a-z0-9_]*\.)?team\s*(?:=|IN\b|<>|!=)`)

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
// There are two exemption lists. reviewedUnscopedSQL holds statements
// that cross teams on purpose -- a reaper, a sweep, the dispatch and
// claim path -- each with its reason. unportedSQL is the backlog of
// families that predate the tenant key and have not moved yet; it
// shrinks and never grows, and porting a family deletes its entries and
// lowers unportedSQLSize in the same commit.
//
// Both are keyed by receiver and function name, so a method moving from
// *Store to *Tenant changes its key and stops being exempt. A function
// that already holds a team cannot be exempt at all; the next test
// enforces that. New unscoped work belongs in reviewedUnscopedSQL with
// its reason, never in the backlog.
func TestTenantSQLScope_EveryTenantTableStatementCarriesTheKey(t *testing.T) {
	seen := map[string]bool{}
	for _, stmt := range tenantSQLStatements(t) {
		tables := tenantTablesTouchedBy(stmt.text)
		if len(tables) == 0 {
			continue
		}
		if statementCarriesTeam(stmt.text) {
			continue
		}
		if exemptFromScope(stmt.key) {
			seen[stmt.key] = true
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
	for _, key := range unportedSQL {
		if !seen[key] {
			t.Errorf("unportedSQL still carries %q, which no longer has an unscoped statement; "+
				"delete the line and lower unportedSQLSize", key)
		}
	}
}

// The backlog shrinks and never grows, so adding to it costs an edit a
// reviewer can see: the list and the pinned size have to move together,
// and a name cannot be on both lists.
func TestTenantSQLScope_BacklogOnlyShrinks(t *testing.T) {
	if len(unportedSQL) != unportedSQLSize {
		t.Fatalf("unportedSQL has %d entries and unportedSQLSize says %d; "+
			"porting lowers both, and nothing raises them", len(unportedSQL), unportedSQLSize)
	}
	seen := map[string]bool{}
	for _, key := range unportedSQL {
		if seen[key] {
			t.Errorf("unportedSQL names %q twice", key)
		}
		seen[key] = true
		if _, ok := reviewedUnscopedSQL[key]; ok {
			t.Errorf("%q is on both lists; it is either reviewed or un-ported, not both", key)
		}
	}
}

func exemptFromScope(key string) bool {
	if _, ok := reviewedUnscopedSQL[key]; ok {
		return true
	}
	return slices.Contains(unportedSQL, key)
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unscoped := len(tenantTablesTouchedBy(c.sql)) > 0 && !statementCarriesTeam(c.sql)
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
		if slices.Contains(tenantTables, name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// safety: reads one statement's text, so a statement joining two tenant
// tables passes on a single predicate; this is the guard's floor, not a
// proof that every table in the statement is scoped.
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
