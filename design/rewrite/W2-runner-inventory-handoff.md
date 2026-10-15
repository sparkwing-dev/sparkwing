# W2 runner observation continuation

<!-- design:begin -->

Started from 549db2fb5. No Git writes. Lead-owned handoffs and amendments are
unchanged. This continues W2-signing-cleanup-handoff.md.

## Implemented and deleted

Runner views no longer call ListAgentClaims, read claim_token_prefix or infer
identity from holder strings. WithRunnerInventory supplies the tenant-scoped
ReadRunners snapshot declared in C2 and agents.go. The same snapshot powers
runner lists, queue headroom and operational runner gauges. Missing adapters
refuse with policy_missing. No legacy read or in-memory fallback remains.

Snapshots retain distinct roots behind equal display names, granted runner
identity/location, activity, enrollment capacity and separate accepted liveness
and headroom times. The handler validates every row before writing a response.
Foreign rows, corrupt numbers and future observation times produce no partial
list. Existing two-minute liveness and one-hour headroom windows remain.
A failed operational read preserves its previous sample and logs the failure.

Deleted the presence, headroom and heartbeat registries, their holder parser,
the obsolete claim body structs and the unused label-recording controller helper.
Claim attention reads the committed profile and LastSeen from C1's existing
ListRegisteredAgents. Accepted machine claim polls now call C1 RecordAgentLabels,
which rechecks live authority and persists filtered labels with the store clock.
Here and launcher credentials bypass that machine-only observation operation.

This session adds 146 and deletes 484 W2 production lines, net -338. Three
registry source files remove 316 lines outright. Five legacy test files remove
637 lines outright. Against main, W2 adds 13189 and deletes 16859 production
lines, net -3670 against the -250 budget. Accounting follows the lead's script:
exclude tests, testdata, acceptance and paths containing contract. The new
untracked Go file is a test, so it adds no production lines.

## Invariants and ports

Invariant: the runner list contains only observations from the authenticated
tenant's snapshot, and display names or holder strings cannot merge authority.
RunnerInventoryHTTPTenantProperty generates foreign tenant/name rows at either
position and proves whole-response refusal. NameProperty generates equal names
and proves activity and liveness stay separate. The HTTP tests use actual member
capability issuance and real SQLite authentication with a fake inventory reader.
Machine/deployment credentials and a member removed after issuance cannot read
it. Empty lists, current/stale headroom, invalid clocks/numbers, private failures
and missing production adapters are covered.

RunnerInventoryMetricsPreservesSamplesOnReadFailure proves distinct live roots
count separately, stale observations do not count, labels come from the same
snapshot and a failed read does not replace either gauge with a partial sample.
The reader is fake. No production inventory-query coverage is claimed.

ClaimAttentionNamesWhyARunWaits and PassWrapsDespiteNewNodes now run on final
SQLite capabilities, execution seals and attention rows. They retain missing
labels, offline, undersized and eligible enrollment plus cursor wrapping while
new jobs arrive. CommittedLivenessProperty generates past/future times against
the existing online boundary. The profile test retains enrolled repository,
pool, platform and capacity preservation. EngineClaimHTTPCommitsRunnerObservations
proves accepted polls persist filtered labels and store time through the real
claim handler. EngineClaimHTTPDeadCredentialProperty generates unknown current-format
and legacy-format presentations and checks canonical refusal without an award.
It also checks root-versus-claim grants and committed revocation.

The port inventory records the five agents tests, the busy-renewal fixture,
registry tests, claim-attention fixtures, queue caller and both auth_claim tests.
Durable renewal/replacement/restart projection remains an explicit integration
obligation. No native credential-matrix or placement cell is newly completed.
No scenario catalog, skeleton, Harness or boundary registry changed.

## Contract changes and real integrations left open

C2 owns the new RunnerInventoryReader, RunnerSnapshot, RunnerObservation and
RunnerHeadroom types. No foreign contract signature or implementation changed.

- W1 must implement Tenant.ReadRunners(ctx) (controller.RunnerSnapshot, error),
  plus Team(), through the declared reader. Read live ancestry/enrollment,
  accepted observations and current awards in one store-clock snapshot. Join
  authority and immutable award identity rather than names or holder strings.
  Keep rows/job references tenant-scoped. Revoked, expired or replaced roots
  cannot refresh or expose another root. An accepted renewal refreshes liveness
  without changing headroom or advertising a slot. Refused renewals change
  neither. The production default currently refuses this missing adapter.
- W8/W9: the final ClaimRequest carries available cores/memory but has no
  queue-depth observation field. Supply the shared ingestion contract before
  wiring actual queue-depth samples. The C2 read preserves that existing feature
  and makes no private wire extension.
- W1's authentication_binding.go still does not project BindingState.CancelReason.
  W2 continues consuming the contract interface, with job intent before run
  intent. Store run/job CancelReason fields and renewal responses already exist.
  No second snapshot or W2 SQL was added.
- The existing console/peer committing authority, roleless session, seeded
  bootstrap/admin-reader, launcher award fence, financial-event settlement,
  source/data/account/enrollment and credential transaction integrations remain
  with their owners as recorded in the earlier handoffs. The lead-decision
  implementations for items 2, 3, 5, 7 and 10 remain in place and their focused
  tests pass. No finished implementation was rebuilt.
- Legacy black-box team-token, claim, identity and log fixtures still need their
  final ports or deletion. The controller selection excludes the inherited
  StreamsOutlive and ReadsStayInside fixtures. No whole-controller pass is claimed.
  The prior W7 upload-lifetime finding is unchanged by this continuation.

No new human decision is required. The local contract interpretation is that
separate roots remain separate observations even when their granted names match.
This removes name-based authority merging. Undo is a revert of this continuation.

Mechanism added: one shared snapshot reader/resolver for the existing runner
views. ListRegisteredAgents alone has no current-award/headroom projection, and
ListAgentClaims uses removed columns and holder parsing. A joined authoritative
read is the smaller final boundary. Existing store observation writes are reused.
No setting, flag, environment variable, cache, service, timeout or customer cap
was added. Lead-owned contracts were not edited.

## Shared files and verification

Outside W2 source, tests/acceptance/queue_reads_http_test.go adds the snapshot fake
to its existing queue fixture. Other shared changes are C2's own contract and
port inventory, CHANGELOG.md, docs/auth.md, docs/observability.md, the migration
guide, generated mirrors and .apidiff/pkg_controller.txt. No W1, W7, W8 or W9
production file changed. The API reference/schema generator ran with no content
diff in its reference/schema outputs.

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp, with go -C
and this worktree's absolute path. Tests use -p 4 and -timeout=180s.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Scoped controller selection, 5.810s: Engine, RunnerInventory HTTP, QueueState,
  ClaimAttention, Policy, ServerPolicy, CredentialIssuance, FinalCredential,
  Browser, OperatorPage, DeleteRunOnALocal, RequestPrincipal, CredentialLabel,
  Audit and FuzzEngine seeds, excluding StreamsOutlive and ReadsStayInside.
- Additional runner metric projection/failure test, 0.215s.
- W2 executable acceptance selection, 16.223s: Credential, CapabilityManagement,
  Data, Discovery, Payment, IdentityLink, LoginHTTP, OAuth, Session, Source,
  Secret, OIDC, Account, RootProvision and QueueRead, plus Credential/Data fuzz
  seeds. Existing native skips remain.
- Capability, apierr, apidiff, apiref and apispec package tests.
- Scoped docs symbol, migration, changelog, privacy and mirror tests.
- Vet for controller, executable acceptance and standalone controller startup.
- API snapshot/reference/schema regeneration, documentation sync/check,
  changelog check and git diff --check. Commentcheck is clean across 2194 files.

The focused tests began red against the absent snapshot contract. Initial
fixture corrections used a member capability because machine roots cannot read
runner inventory. The production policy was not widened.

TypeScript source/setup is unchanged. Its prior native Linux pass was not rerun.
No Sparkwing binary, pipeline, provider network, native socket, deployment or
whole-repository test suite ran. The lead owns review, commits and landing.
Default Git identity remains Korey Gambill and core.worktree is unset. Session
scratch is removed after the final checks. The shared Go cache is retained.

<!-- design:end -->

STATUS: CONTINUE
