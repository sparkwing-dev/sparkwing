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
	"(*Store).DueCardCharges": "the payment worker expires every team's stale pay-now attempts in one pass " +
		"under the ledger lock",
	"startDueAttemptsTx": "the payment worker starts the due attempts of every team's open charges; " +
		"each row it touches is named by its global id",
	"(*Store).SettleCardPayment": "a payment names its charge by global id, and the charge row it reads " +
		"is checked against the team the payment names",
	"(*Store).FailCardAttempt":     "a decline names its attempt by global id; the team is read from that row",
	"(*Store).DropCardAttempt":     "an attempt that never reached Stripe is closed by its global id",
	"(*Store).RecordAttemptIntent": "the worker binds a payment to the attempt it names by global id",
	"(*Store).RecordPaymentWarning": "a warning names only its payment, so the team is what the payment's " +
		"grant or attempt row says",
	"(*Store).DueCardRefunds": "the payment worker lists every team's queued refunds in one pass",
	"(*Store).ReportCardRefund": "Stripe names a refund by its payment and refund ids alone; the team is what " +
		"the queued row says",
	"globalRunnerRefusal":                 "the global concurrent runner cap counts live claims from every team",
	"globalRunsPerHourRefusal":            "the global hourly cap counts runs from every team",
	"(*Store).NodeClaimFenceNodeForRun":   "the run ID is global, and the query matches its exact claimant and generation",
	"(*Store).PruneExpiredUploads":        "the hourly storage pass releases expired pending uploads for every team",
	"(*Store).PruneStorageCommitReceipts": "the hourly storage pass drops receipt rows past the retry window for every team",
	"(*Store).PruneExpiredCacheObjects": "the controller's leased hourly storage pass deletes expired cache rows " +
		"for every team after a successful bucket listing; scoping this delete to one team would leave another team's old rows visible",
	"(*Store).expiredReservationRows": "the sweep finds which teams hold expired reservations; each release " +
		"then runs under that team's own row lock",
	"(*Store).StorageMarks":          "the storage pass reconciles every team's count of one store from one bucket listing",
	"(*Store).TeamsOverFreeLogShare": "the storage pass finds every team whose logs are past its share, slot or no slot",
	"lockCommittedTx":                "the storage pass reconciles every team's count of one store from one bucket listing",
	"applyAgentNameIndexMigration": "the schema migration retires expired agent tokens and refuses duplicate agent names in every team " +
		"before it builds the index that spans them",
	"(*Store).PruneDownloadDays": "drops every team's download days past the window, which is a deployment-wide " +
		"retention, not one team's data",
	"expiredClaimRunsTx": "the expired-claim reaper finds every team's runs holding a lapsed claim, " +
		"to lock them before their nodes",
	"(*Store).expiredDispatchRuns": "the expired-claim reaper finds every team's controller-dispatched runs " +
		"holding a lapsed claim; each is then settled under that team's own run row",
	"runOwnerTx":             "asks which team owns an id, so an answer scoped to the asker is no answer",
	"(*Store).NodeOutputKey": "the run ID is global, and the key only names an attempt the upload's own check binds",
	"(*Store).RecordLocalOutput": "a laptop store records an output its own process wrote for a node it holds; " +
		"the run ID is global and the row takes that node's team",
	"(*Store).PaidGrantTeam": "asks which team a payment id was granted to, so a refund that names only " +
		"the payment reverses it in that team; a payment id is unique across teams",
	"(*Store).claimScope": "asks which team a claim credential belongs to, so an answer scoped to " +
		"the asker is no answer; it is the read every other claim predicate is built from",
	"(*Store).readClaimCandidates": "the team predicate comes from claimTeamWhere at run time; " +
		"a scope that is not exactly one team is refused there",
	"(*Store).bumpMismatchedNodes":  "shares the claim scan's runtime predicate and its refusal",
	"executorPrepareCandidateQuery": "shares the claim scan's runtime predicate and its refusal",
	"(*Store).awardScannedNodeTx": "the award carries the same runtime predicate; the read that follows " +
		"it is of the row the award just proved in team",
	"(*Store).ClaimNamedNode": "shares the claim scan's runtime predicate and its refusal",
	"refuseEventOverLimitsTx": "reads one run's event counters for a cap on that run; the run id " +
		"names one team's row and the fence checked before it proves the caller holds that run",
	"backfillRunEventUsageTx": "a v52 migration that counts every run's events onto that run's own row",
	"invalidateNodeMeasurementHistory": "a v89 migration that marks every node's resource history in every team as " +
		"predating labeled readings; each marker row takes its node's own team",
	"applyMetricSampleKindMigration": "a v89 migration that labels every team's readings that carried command CPU time",
	"applyTriggerCreditCursorMigration": "a v73 migration check that refuses the upgrade while any team " +
		"holds an open trigger reservation, because the schema is the deployment's and a reservation open in any team " +
		"predates the paid cursor the migration adds",
	"duplicateTeamGrantReferences": "a v52 migration check that groups every team's grants by team to find " +
		"the rows the per-team reference key would refuse",
	"(*Store).runnerTeams": "asks which team each live runner's credential belongs to, so an " +
		"answer scoped to the asker is no answer; it is how another team's runner is dropped",
	"(*Store).RecordAgentLabels": "writes the one credential the claim authenticated with; a token " +
		"prefix is unique across teams",
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
	"selectGitCredentialsTx":                 "reads every team's git credential so one key rotation reseals the whole table, and rewrites each row under its own team",
	"(*Store).secretsNotSealed":              "finds every team's secret still held as plaintext or a pre-team envelope, so the startup reseal binds each to its own team",
	"(*Store).SampleSealedSecrets":           "samples envelopes from any team to prove the configured key opens them before the startup reseal writes anything",
	"(*Store).UnlinkIdentity":                "ends one account's sessions in every team, because the sign-in it removed could have opened any of them",
	"(*Store).lookupSession":                 "resolves the globally unique digest of the presented session before its team is known, and renews only that session",
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
	"freeSlotOpenTx":     "a reservation asks whether any slot is left, so it counts every team's",
	"(*Operator).ListCronSchedulesAcrossTeams": "the controller's tick evaluates every team's schedules and resolves and " +
		"launches each one through its own team's handle",
	"disputeHoldTx": "asks which payment and team a dispute's hold names, so a hold or reversal naming it for " +
		"another team is refused; a dispute id is unique across teams",
	"(*Store).DisputeTeam": "asks which team a dispute's hold is on, so an operator's release that names only the " +
		"dispute answers with that team; a dispute id is unique across teams",
	"applyMigrationSQLite": "the SQLite schema ladder rewrites every team's rows, and its early steps run before the team " +
		"column exists",
	"(*Store).applyMigrationPostgresTx": "the PostgreSQL schema ladder rewrites every team's rows, and its early steps run " +
		"before the team column exists",
	"applyFleetMigrationSQLite":   "a schema step that backfills every team's node offer and attempt columns",
	"applyFleetMigrationPostgres": "a schema step that backfills every team's node offer and attempt columns",
	"bridgeLegacyFleetSQLite": "bridges a database from an older release by numbering every team's nodes, as the step it " +
		"replaces did",
	"validateLegacyFleetShape": "reads no row: its WHERE 1 = 0 probes only prove the bridged tables carry the expected " +
		"columns",
	"addNodeMetricsRunCascadePostgres": "a schema repair that drops node metric rows whose run is gone in any team before " +
		"it adds the cascade",
	"backfillAgentLossRetryNodeSourcesTx": "a schema step that records every team's pre-snapshot agent-loss retries as " +
		"deny-all, before the source table carries a team",
	"backfillRunAnnotationRollup": "an open-time backfill of every team's run annotation rollup, which also runs as schema " +
		"v1 before the team column exists",
	"gatherRunAnnotations": "reads one run's node and step annotations for the open-time rollup backfill, which names the " +
		"run from its own scan and predates the team column",
	"scrubSecretInputHashes": "a schema step that strips secret input hashes from every team's run invocations",
	"rehashSessions": "a schema step that drops every team's sessions minted under the retired digest, so each signs in " +
		"again",
	"duplicateGrantReferences": "a migration check that finds grant references repeated anywhere, because the index it " +
		"gates is created over the whole table",
	"duplicateTokenPrefixes": "a migration check that finds token prefixes repeated anywhere, because a token prefix is " +
		"unique across teams",
	"(*Store).reapExpiredTriggers": "the trigger-lease reaper returns every team's lapsed claims to the queue, because a " +
		"sweep that only reaped one team would leave the rest held",
	"(*Store).reapQueueExpiredRuns": "the queue-deadline sweep ends every team's runs no claimant took; each write names a " +
		"run its own scan selected by global id",
	"(*Store).reapStalePendingRuns": "the stale-run sweep fails every team's pending runs whose trigger is gone or " +
		"finished; each write names a run its own scan selected by global id",
	"(*Store).reapStaleRunningRuns": "the stale-run sweep fails every team's running runs no orchestrator heartbeats",
	"(*Store).reapTimedOutApprovals": "the approval sweep resolves every team's approvals past their timeout; each write " +
		"names an approval its own scan selected",
	"(*Store).reconcileOrphanedLocalRuns": "the orphan sweep fails every team's runs whose orchestrator stopped " +
		"heartbeating, and cancels pending nodes left under any terminal run",
	"(*Store).orphanedRunsQuery": "the orphan sweep's scan of every team's running runs, with each run's own nodes' " +
		"heartbeats",
	"(*Store).cascadeOrphanedNodes": "fails the open nodes of a run an orphan sweep selected by global id from its own " +
		"scan",
	"(*Store).failNodesInRun": "the trigger-claim sweep fails the open nodes of a run it reaped; the global run id comes " +
		"off that sweep's own scan",
	"(*Store).failStaleQueuedNodes": "the queue-wait sweep fails every team's ready nodes no runner claimed in time",
	"(*Store).recoverExpiredNodeClaims": "the expired-claim reaper fails and retries every team's nodes whose runner lease " +
		"lapsed; each write names a node its own locked scan selected",
	"(*Store).ReapExpiredNodeClaims": "clears every team's lapsed node claims in one locked pass; each write names a node " +
		"its own scan selected",
	"(*Store).expirePendingAgentLossRetriesTx": "expires every team's agent-loss retries past their deadline, " +
		"because a claim that expired only its own team's would leave the rest queued past it",
	"(*Store).ListExpiredClaims": "the local trigger consumer's sweep lists every lapsed trigger claim on the store before " +
		"judging each one",
	"(*Store).FinishLapsedClaim": "closes a lapsed trigger claim the local consumer's sweep selected by global id; the run " +
		"it checks is the trigger's own",
	"(*Store).PruneRunsOlderThan": "the run retention window is the deployment's, so the prune finds every team's expired " +
		"runs",
	"(*Store).PruneEgressUsage": "drops every team's egress months past the window, which is a deployment-wide retention",
	"(*Store).ExpireSessions":   "the identity prune deletes every team's expired sessions",
	"(*Store).sweepTable": "the retention sweep deletes every team's events and node metrics past the window that the " +
		"run's own retention allows",
	"(*Store).expireRunStorage": "the storage retention pass frees one expired run's events and usage, named by the global " +
		"run id and principal its own scan selected",
	"(*Store).retainingPrincipals": "the storage pass lists every team's retaining principals with their teams and charges " +
		"each against that team",
	"txLiveRunningRunIDs": "the concurrency reapers ask which of the runs they found in any team are still live",
	"ownedTeamsTx": "lists the teams one account belongs to as owner, with each team's own owner and member counts, so the " +
		"account deletion can tell sole ownership from shared",
	"(*Store).ExpiredOutputRuns": "the output retention sweep finds every team's expired or deleted runs holding outputs, " +
		"with their teams, and frees each in that team",
	"(*Store).ReconcileFreeEventBytes": "the storage pass recounts every slotted team's event bytes, each from its own " +
		"team's runs",
	"(*Store).prepareNextExecutorClaim": "reads the candidate nodes the claim scan's runtime predicate already scoped to " +
		"the claimant's one team",
	"(*Store).loadExecutorPreparePlans": "reads the run rows of candidates the claim scan already scoped to the claimant's " +
		"team, and returns each run's team",
	"(*Store).loadExecutorPrepareProfiles": "the team predicate is built at run time, one per profile identity, from the " +
		"team each candidate's run row carries",
	"(*Store).rejectUnattestedExecutorOffer": "reads the node an offer names after assertClaimantOwnsNode proved it in the " +
		"claimant's team",
	"(*Store).recordExecutorOfferAt": "records an offer on a node assertClaimantOwnsNode proved in the claimant's team; " +
		"the offer row takes the run's team, and the conflict checks span teams because an executor's slots and " +
		"reservations are the deployment's",
	"claimedExecutorOffer":     "reads the node an offer names after assertClaimantOwnsNode proved it in the claimant's team",
	"nodeExecutorOfferCountTx": "counts the offers on one node the offer path already proved in the claimant's team",
	"(*Store).awardBestExecutorOffer": "awards a node the offer path or a team-bounded finalize route named by global id; " +
		"every offer on it was recorded after assertClaimantOwnsNode, and the winner's other offers are keyed by its " +
		"own credential",
	"(*Store).expireNodeExecutorOffersTx": "expires lapsed offers on the one node an award names by global id",
	"(*Store).expireConflictingExecutorOffersTx": "expires lapsed offers that hold the same executor slot or reservation, " +
		"which are the deployment's and shared by every team's offers",
	"(*Store).finalizeExecutorClaimRoundAt": "closes the offer round of a node named by global id under the team boundary " +
		"of the finalize route or by the local fleet's own coordinator",
	"(*Store).claimReadyNodeForExecutorTx": "claims a node the claim scan's runtime predicate selected in the claimant's " +
		"team; the slot and budget counts span teams because an executor's capacity is the deployment's",
	"(*Store).executorEligibility": "counts an executor's live claims in every team, because its capacity is the " +
		"deployment's and every team's claims use it",
	"(*Store).executorEligibilityTx": "counts an executor's live claims in every team, because its capacity is the " +
		"deployment's and every team's claims use it",
	"(*Store).loadExecutorUsage": "loads every executor's live claim usage across teams, because executor capacity is the " +
		"deployment's",
	"loadExecutorUsageTx": "loads every executor's live claim usage across teams, because executor capacity is the " +
		"deployment's",
	"(*Store).schedulingSummaryTx": "reads the node and run a claim, offer or award names by global id, and returns the " +
		"run's team for the checks that follow",
	"(*Store).ValidateExecutorClaimReservation": "matches the node's live claim against the exact claimant credential and " +
		"reservation, and a token prefix is unique across teams",
	"(*Store).buildNodeExecutionPolicyTx": "builds the execution policy of the node a scoped claim just awarded, from that " +
		"node's own run and plan",
	"nodeChargeTx": "reads the team, pipeline and plan of the run a charge names by global id, so the charge lands in that " +
		"run's team",
	"(*Store).TriggerClaimFenceIsLive": "matches the trigger's live claim against the exact claimant credential and " +
		"generation, and a token prefix is unique across teams",
	"(*Store).NodeClaimFenceIsLive": "matches the node's live claim against the exact holder, credential, reservation and " +
		"generation, and a token prefix is unique across teams",
	"(*Store).NodeExecutionAttemptIsLive": "matches the node's live claim and open attempt against the exact holder, " +
		"credential and generation, and a token prefix is unique across teams",
	"(*Store).NodeExecutionAttemptBelongsToLiveClaim": "matches the node's live claim and its attempt against the exact " +
		"holder, credential and generation, and a token prefix is unique across teams",
	"(*Store).TriggerExecutionAttemptIsLive": "matches the trigger's live claim and open attempt against the exact " +
		"claimant credential and generation, and a token prefix is unique across teams",
	"(*Store).TriggerExecutionAttemptBelongsToLiveClaim": "matches the trigger's live claim and its attempt against the " +
		"exact claimant credential and generation, and a token prefix is unique across teams",
	"(*Store).PrincipalHoldsNodeClaim": "matches the node's live claim against the exact claimant credential, and a token " +
		"prefix is unique across teams",
	"(*Store).PrincipalHoldsRunClaim": "matches a live claim on the run's nodes against the exact claimant credential, and " +
		"a token prefix is unique across teams",
	"(*Store).PrincipalHoldsTriggerClaim": "matches the trigger's live claim against the exact claimant credential, and a " +
		"token prefix is unique across teams",
	"(*Store).PrincipalHoldsProfileClaim": "matches a live claim of the exact claimant credential on the pipeline's runs " +
		"or triggers; a token prefix is unique across teams and a claim only reaches its own team's work",
	"(*Store).ClaimedRunFor": "asks which team a run the exact claimant credential holds live work in belongs to, so an " +
		"answer scoped to the asker is no answer; a token prefix is unique across teams",
	"(*Store).ClaimedRunsFor": "asks which teams' runs the exact claimant credential holds live work in, so an answer " +
		"scoped to the asker is no answer; a token prefix is unique across teams",
	"lockRunRow": "the expired-claim reaper and the trigger lease beat lock a run row by the global id their own read " +
		"selected, in the order the claim path names, and read nothing back",
	"(*Operator).RunTeam": "asks which team owns a run id a sweep found across teams, so the sweep acts on it through that " +
		"team's handle; an answer scoped to the asker is no answer",
}

// safety: this list shrinks and never grows; porting a family deletes
// its entries and lowers unportedSQLSize in the same commit, so an entry
// cannot be added without a reviewer seeing the number move.
var unportedSQL = []string{
	"(*Store).AcknowledgeNodeExecutionStart",
	"(*Store).AddNodeMetricSample",
	"(*Store).CacheExcludedCounts",
	"(*Store).ComputeAlarmState",
	"(*Store).ComputeUsage",
	"(*Store).ConsumeNodeBounce",
	"(*Store).CountActiveRunners",
	"(*Store).CountNodesByQueueState",
	"(*Store).CountPendingNodes",
	"(*Store).CountUsers",
	"(*Store).CreateFirstUser",
	"(*Store).CreateSession",
	"(*Store).CreateTokenIfNoneExist",
	"(*Store).CreateUser",
	"(*Store).CreditLedgerTotals",
	"(*Store).DeleteSession",
	"(*Store).DeleteUser",
	"(*Store).FinishNodeExecutionAttempt",
	"(*Store).finishNode",
	"(*Store).ListCreditCharges",
	"(*Store).ListCreditGrants",
	"(*Store).ListEgressUsage",
	"(*Store).ListLegacyAgentClaims",
	"(*Store).ListNodeBounces",
	"(*Store).ListNodeMetricsPage",
	"(*Store).ListStorageQuotas",
	"(*Store).ListTokens",
	"(*Store).ListUsers",
	"(*Store).NodeSettlement",
	"(*Store).PendingNodeBounce",
	"(*Store).RecordEgressUsage",
	"(*Store).RequestNodeBounce",
	"(*Store).ResetNodeForAutoRetry",
	"(*Store).RevokeNodeReady",
	"(*Store).RevokeToken",
	"(*Store).SetNodeArtifactManifestCharged",
	"(*Store).SetStorageAllowance",
	"(*Store).SetStorageQuota",
	"(*Store).SetTokenMetered",
	"(*Store).StorageRetainedBytes",
	"(*Store).StorageUsageFor",
	"(*Store).TokenMetered",
	"(*Store).TopStorageTeams",
	"(*Store).VerifyUser",
	"(*Store).acknowledgeTriggerExecutionStart",
	"(*Store).cancelMeteredNode",
	"(*Store).chargeNodeTx",
	"(*Store).chargeStorageTx",
	"(*Store).createAgentLossRetryTx",
	"(*Store).finishLocalNodeExecutionAttempt",
	"(*Store).finishTriggerExecutionAttempt",
	"(*Store).lookupUser",
	"(*Store).markNodeReady",
	"(*Store).mergeAgentLossRetryTx",
	"(*Store).mintCSRFKey",
	"(*Store).requiredAgentLossRetryNodeSourceTx",
	"(*Store).reserveNodeCreditsTx",
	"(*Store).rotateToken",
	"(*Store).selectTokensByPrefix",
	"(*Store).startLocalNodeExecutionAttempt",
	"(*Store).storageQuotaRow",
	"clearCreditExhaustionAnchorTx",
	"creditExhaustionAnchorTx",
	"livePrefixesForPrincipal",
	"loadAgentLossRetryNodeSourceTx",
	"persistAgentLossRetryNodeSourceTx",
	"selectTokensByPrefixTx",
	"snapshotAgentLossRetryNodesTx",
	"stampCreditExhaustionAnchorTx",
	"storageQuotaForTx",
	"tokenMeteredTx",
}

// safety: pins the backlog's length so it can only shrink.
const unportedSQLSize = 71

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
		if slices.Contains(tenantTables, name) && !slices.Contains(out, name) {
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
