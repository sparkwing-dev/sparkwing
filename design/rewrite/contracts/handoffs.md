# Cross-area handoffs

Each worker acts on every entry naming its area. Entries come from area reviews and fixes; mark an entry done in your report.

## From W7 controller maintenance cutover

**Completed W7 maintenance integration.** The retained-data worker now passes
its configured ObjectBackend to W1's real MarkBlobTombstones and
CollectBlobTombstones APIs under a renewed maintenance fence. Configured
filesystem objects also run class expiry after renewal. Standalone S3 startup
installs the backend through WithDataObjects and the declared DataController
fields. Authenticated data methods remain W2's implementation.

Deleted the separate bucket-reconciliation worker, free-share log pruning,
listing-derived tenant accounting, storage_pass health projection and unused
teamblob measurement API. The cut removes 881 Go lines, including 432 obsolete
test lines. Physical collection errors do not skip retained-storage billing.
A signed-license regression proves the zero-rate watermark still resets after
backend deletion fails. The existing key-containment property has no S3 fixture. A real
SQLite/filesystem controller property checks 64 generated tenant objects,
references, tombstone grace and replay. A foreign lease prevents physical expiry.
No new acceptance cell was added.

**W1b/W2 staging cleanup contract needed.** Quota expiry is wired through W1's
marking transaction. Expired or abandoned pending objects are separate from live
CAS blobs. W7 has no leased operator seam returning their staging keys or
invoking ObjectBackend.DeleteObject. Declare that seam before physical cleanup;
W7 will not duplicate uploads SQL or infer authority from bucket listings.

**W2/W3/W8/W9 remaining construction.** The existing LogTransport and
SegmentRecorder need bound client construction and the controller wire needs a
segment-commit operation. Authenticated data methods, filesystem startup/route
mounting and fresh host StorageBinding construction remain open. The older
output/log handlers, clients, host sinks and raw backends remain live until that
cutover. No new foreign contract or compatibility protocol was added.

Build, storage tests, the new controller integration tests, vet, commentcheck,
current-diff sleepcheck and embedded-doc drift pass. TestStorage passes with
81 passing events and 424 native/platform skips. Sleepcheck against main
still reports internal/limits/deadline_test.go:58/:66 and
sparkwing/sparkwingtest/host_test.go:214, outside W7's change. See
[w7-storage-report.md](w7-storage-report.md) for command scope and final acceptance.

## From W7 merged-store wiring

**Completed W7 record-layout integration.** Output and artifact capture now use
W1's persisted exact-attempt layout through `blob.ResultRecordKey`. Memo writers
and exact readers use `MemoRecordName=output`. W1 admission, copy destinations
and result attachment consume the shared builder. The former
`store.ResultBlobKey` is deleted and every caller is migrated. C1's W7 layout
paragraphs now match these real persistence APIs. No schema changed.

Invariant: a result key identifies the exact tenant/run/job/attempt/generation,
and a caller cannot select another run/job or supply the result scope key.
A 96-case generated property covers key separation and refused selectors.
The existing filesystem/S3 archive cells also write output, capture artifacts,
commit memo, reopen SQLite, materialize memo and stage artifacts using real
`Tenant.BlobRecords` and verified physical manifests. Only authenticated binding
and physical S3 service are fixtures. No component-only cell was added.

**Completed W7 physical log wiring.** `Tenant.RecordSegments` now uses
`blob.LogObjectKey`, and persisted segment numbering starts at one. The duplicate
`store.LogSegmentObjectKey` and its callers' old layouts are removed. Store and
acceptance fixtures use the common slots. The existing filesystem-records cell
is unskipped. The former S3 component cell now uses real tenant segment commits,
reopens and reads through the committed reader, and is named `s3-records`.
Both backends exercise lost PUT replies, exact replay, offsets, retained bytes
and revoked-reader refusal. This closes the physical-log-slot handoff below.

**Remaining W1/W2/W3/W8/W9 integration.** Authenticated DataAuthority, BindData,
NewDataClient and policy-router methods still panic as unimplemented. Hosts
still need fresh StorageBinding values. Repo-reader exact lookup still needs
server-derived tenant/default-ref selection. The controllerwire client has no
segment-commit method or factory for the declared LogTransport. W2/W9 must bind
those existing interfaces before the older output/log clients and their host
callers can be cut over. W7 has not invented a second authentication protocol.
Physical collection and filesystem expiry still need maintenance construction.

**DESIGN: record mutation authorization.** The known W1b regression still fails.
CommitRecord and CopyMemo can persist one output and expose a read set after a
namespace callback crosses a job lease, leaf capability or ancestor expiry.
W7 reproduced all six cases and left the mechanism unchanged under the brief's
two-strikes rule. The lead's transaction-local publication redesign remains open.

Full TestStorage passes with 81 passing events and 424 native/platform skips.
Whole-repository build, all 173 package compilations, storage tests, scoped store
and host tests, vet, commentcheck, sleepcheck and embedded-doc drift pass.
The store selection excludes that separately reproduced red regression.
The two duplicate key builders remove 23 legacy definition lines. The remaining
output/log transports are still live and remain an unfinished hard cut.

## From W7 amended merge and manifest fixtures

**Completed W7 merge resolution.** All seven conflicted working paths are
resolved. Amended C1 and W1 evidence remain alongside W7 behavior and migration
notes. The obsolete grant/upload test deletions are preserved. API generation
also removed snapshot entries resurrected by the merge for W7's earlier cuts.
All generated snapshot contents match the merged implementation. The orphan
`.apidiff/pkg_runner.txt` still needs W8/lead cleanup. The index remains unmerged
because the swarm brief prohibits Git writes. The lead must stage and commit.

**Completed W7 fixture migration.** Both physical manifest-record cells failed
with `holder_rights` because W1 now requires a terminal imported attempt and the
W7 fixture still inserted pending work. Twelve generated cases per backend now
refuse an unfinished attempt even when its node is terminal, verify that no
reservation remains, finish that exact attempt, and retain the earlier manifest
metadata, unrelated-receipt and reopen assertions. Both cells pass, including with the race detector. Full storage acceptance
passes with 79 events and 425 skips. No production authority changed.

**W2 build-trust fixture.** The retained controller test now uses
`Tenant.TrustLocalBuilds`, so all 173 repository test packages compile. Running
`TestTeamOwnerCanChooseWhetherCloudTrustsLocalBinaries` still fails during
`newAppFixture` with `no such table: tokens`, before reaching the handler. Migrate
that authentication fixture to C2. The W1 record/memo/log-slot and W2 data-client
handoffs below remain open. Removing only the physical-log skip in a temporary
overlay still fails on segment 1 with the signing-slot mismatch.

## From W7 publication byte components

**Completed W7 component evidence.** `TestStorage` cells
`storage-publish-one-way/filesystem-components` and `s3-components` each pass
128 generated imported attempts and payloads, including empty output. They join
output writes, artifact capture, signed transfers, immutable physical publication,
verified reads and artifact staging. Ten altered uploader selectors per case fail
before upload. Refused record updates preserve prior records, readable output
and local files. Both cells pass with the race detector.

Invariant: only the bound imported attempt can publish output or artifact bytes,
and a refused record update cannot replace an earlier committed result.

Signer/record callbacks are contract fakes because W2's authenticated binding
remains unimplemented. W1/W2/W3/W9 still own published intake, local provenance,
`trust_local_builds`, terminal results, log transport and separate CLI publication
failure. These component cells do not close the native publication proof.
Whole-repository build, storage tests, focused vet, documentation drift checks,
commentcheck and sleepcheck pass. Full `TestStorage` reports 76 passing events
and the existing 425 skips. No contract or runtime API changed. No additional
legacy deletion is claimed while its final production bindings remain open.

## From W7 remote archive integration and log-reader cut

**Completed W7 fix.** The signed filesystem and S3 remote archive proofs first
failed with `empty or invalid record namespace component`. `NewRemoteReader`
leaves ref selection to the server, but `RemoteCache.Depcache` applied the
write-namespace validator to that empty ref. Remote memo and depcache lookups
now validate logical key fields and leave ref resolution to the existing
verified-default-only record query. Their public signatures are unchanged.

Invariant: a local ref cannot select remote bytes, failed verification preserves
the destination, and a local save cannot publish into the shared cache.
The ref-selection fuzz test passes 1,000 cases. New `TestStorage` cells
`storage-local-remote-cache-opt-in/filesystem-archives` and `s3-archives` each
pass 128 generated cases through signed upload, immutable publication, trusted
record lookup, verified download, archive extraction and local save/restore.
Forged record scopes, equal-length corruption and missing objects fail. Local
saves restore with the remote object still absent. Both cells pass with race
detection. Record/authentication callbacks are fakes, so these are component
proofs. W2/W3/W9 still own authenticated transport and host/profile construction.

**Completed W7 cut.** Removed the unused log-reader `Append` and `DeleteRun`
methods, their controller/client adapters and `storage.SafeLogIDs`. Runtime
files delete 41 lines and add two replacement GoDoc lines. The internal
run-deletion path remains the owner of physical log cleanup. Shared runtime
files touched are `pkg/controller/log_reads.go` and
`pkg/controller/client/logs.go`. The storage snapshot is regenerated. The client
snapshot removes only those two methods. Its unrelated stale
`ResetNodeForAutoRetry` entry remains for W3/W9/lead.

Whole-repository build, storage tests, focused vet, both document drift guards,
commentcheck and sleepcheck pass. `TestStorage` reports 74 passing test events
and 425 skips. The existing W1 result/memo/log layout and W2 data-adapter gaps
remain open. No cross-area contract change or new runtime mechanism is needed.

## From W7 merge resolution and log conformance cut

**Completed W7 cuts.** The acceptance dispatcher keeps both storage checks and
the incoming TypeScript boundary check. Its conflict markers are removed. The
index still lists it as unmerged because the swarm brief prohibits Git writes.
The lead must stage the resolved file before completing the merge.

The append/delete `conformance.TestLogStore` suite, its only caller
`segmentLogStore`, and its unused deletion helper are removed. That deletes
384 legacy code/test lines. Filter assertions now call the real sink and reader
across immutable cuts. Seven cases cover full reads, head, tail, grep, line
ranges, combined filters and empty selections. Invalid ranges also fail.
The suite's fake deletion and stream opt-out supplied no production coverage.
Physical expiry and committed-reader coverage remain in their existing tests.
The API snapshot, versioning page, migration guide and embedded docs match the cut.

W7 package tests, vet, commentcheck and the whole-repository build pass.
`TestStorage` still reports 72 passing events and 425 skips. No native cell is
newly claimed. W1's record layouts, W2's authenticated data adapters and W3/W8/W9
host/profile construction remain open as detailed below. No new contract change
or product decision is requested.

**W4/W9 docs symbol cutover.** `go test -p 4 ./pkg/docs -run
'^TestDocsNameOnlySymbolsThatExist$' -count=1` fails on 17 removed Go SDK
symbols: Await, CacheDir, CacheKey, CacheKeyFn, DefaultCacheTTL, DescribePipeline,
JobApproval, JobFanOutDynamic, MaxCacheTTL, Memoize, NoCache, NpmCache, Resolve,
TTL, ToolCacheDir, ToolSlot and WhenRunner. The affected pages are
`docs/sdk-reference.md`, `docs/caching.md`, `docs/authoring-pipelines.md`,
`docs/pipelines.md`, `docs/sdk-inputs.md`, `docs/cli.md` and
`docs/local-execution.md`. Both the SDK and these pages are unchanged relative
to W7's HEAD. W4/W9 should migrate the pages to the final SDK surface and sync
the embedded copies. W7's two embedded-document drift guards pass.

## From W7 tagged-sink acceptance and unused backend deletion

**Completed W7 cuts.** The unused `fs.LogStore` and `fs.NewLogStore`, their
append test, example and mixed-fixture tests are deleted. Their only callers
were tests. Concurrent numbered writers now run through the real tagged sink,
filesystem and S3 signed transfers, spool and committed reader in
`storage-log-segments-and-fallback/filesystem-sink` and `s3-sink`. Each backend
receives 128 generated cases. Duplicate delivery, writer ordering, live offsets,
lost PUT responses, byte-offset reads, generation mismatch and spool cleanup
pass, including under the race detector. Authorization and records are fakes.

The dead `artifact_store_error` diagnostic is removed from the daemon config,
handshake, probe, CLI and wire snapshot. Its source docs and embedded copies
are updated. The field had no producer after cache-backend construction was
removed. W8/W9 should keep it deleted while migrating the remaining daemon
surface. No replacement field or fallback was added.

Storage acceptance reports 72 passing events and 425 skips. W1's existing
result/memo and log-slot layout handoffs, W2's authenticated bindings and
W3/W8's host construction remain open. These two sink cells do not close
native acceptance. Full bincache/docs HTTP fixtures and daemon socket tests
are unavailable in this sandbox. Their focused storage, drift and display
checks pass.

## From W7 archive integration and cache delivery cleanup

**W7 archive transactions now pass with both physical backends.**
`storage-memo-and-depcache-scope/filesystem-archives` and `s3-archives` join
signed transfers, SQLite upload reservations, physical publication, receipt
settlement, record commits and reopened archive restores. Each backend receives
128 generated payloads. Forged repo/ref/attempt selectors fail. Equal-length
corruption and missing objects preserve the destination. Failed work cannot
replace a cache record. Successful cold work can publish a replacement.
Authority and claim intake are seeded. The signing adapter is a contract fake,
so W2 authentication and W3/W8 host construction remain open.

**Cache image fixture cut completed.** The current release fixtures now expect
only controller and runner images. Both previously failing release tests pass.
Three commands targeting the removed Git cache package were deleted from
`bin/pre-release-test.sh`. Historical release-asset matrices remain unchanged.
W9/lead should retire or migrate that standalone script's other obsolete package
paths. Its syntax passes, but the full script was not run and is not claimed as
a release check. The current release scripts were exercised with a fake registry.

## From W7 whole-branch verification

**W6/W4 test timing.** `go run ./internal/sleepcheck -base main .` reports
three violations outside W7's changed files: `internal/limits/deadline_test.go`
at lines 58 and 66, and `sparkwing/sparkwingtest/host_test.go` at line 214.
W6 owns the deadline fixture. W4 owns the host fixture. Port their timing to
signals or `testing/synctest`. Whole-repository build and compile-only tests
pass. This checker result does not establish a product runtime failure.

## From W7 materialization and measurement

**W1/W2/W3/W8 integration remains open.** The new
`storage-memo-materialization-failures/filesystem-components` and
`s3-components` cells join W7's output, capture, memo and staging implementations
through real signed byte transfers. Their record transactions are contract
fakes because W1's result layout and W2's authenticated binding remain open.
They preserve the failure assertions behind the follower-output,
manifest-commit and retry-artifact tests. They do not close the native host
ports or prove a store-owned terminal transition. The existing W7 physical-log,
manifest-admission and host-binding handoffs below still apply.

**Raw factory cut completed.** `storeurl.OpenArtifactStore`,
`storage.Usage` and the unused `Server.WithArtifactStore` fallback are deleted.
`OpenMeasurementStore` and `Server.WithBucketUsage` now take the read-only
`storage.UsageReporter` contract. The controller's configured measurement path
still uses the shared S3 client constructor, retains its page cap and bypasses
the serving request budget. The repository builds without the removed APIs.
Remaining raw backends serve the old log routes and still need deletion at
the final tagged-log cutover.

## From W7 physical log integration

**W1 log slot contract.** W7 now provides `blob.LogObjectKey(subject, class, index)`
and the shared prefix/filename helpers in `internal/blob/log_keys.go`. They have
no store dependency. Index zero selects the manifest. Segment indexes start at
one in the signed log contract. The `logseg` copies are deleted and all
their callers use the physical owner.

The new `CheckBlobLogRecords` proof joins the real signed filesystem uploader to
`Tenant.RecordSegments`, then reopens SQLite and checks verified offset reads and
revocation. It fails on the first segment with
`log segment object belongs to another signing slot`. `LogSegmentObjectKey`
instead derives `teams/<team>/logs/<class>/<run>/<subject-hash>/<index>`, and
`appendSegmentsTx` starts records at zero. W1 must settle the record-slot contract
before this integration can pass. W7 recommends using the shared physical key
owner, starting records at one and porting the store-only zero-based fixtures.
That avoids a second layout or an index translation. Remove the explicit skip in
`checkStorageFilesystemLogRecords` when that boundary passes. Command:
`go test -p 4 ./tests/acceptance -run '^TestStorage/storage-log-segments-and-fallback/filesystem-records$' -count=1`.

The initial failure was executed before the explicit skip was added. The cell
remains blocked and does not count as passing acceptance evidence. W7 changed
its own helper contract only. W1's persistence implementation is untouched.

W7 reproduced the same first-segment refusal after resolving the integration
merge, using a temporary Go overlay to remove only this skip. The new
`s3-components` cell passes with real signing, lifecycle checks, SDK transfers,
uncertain-PUT verification and offset reads. Its record callbacks are fakes,
so it does not close the SQLite record-slot mismatch.

## From W7 manifest admission

**W2 verified manifest binding.** `storage.BlobManifests{Objects: objects}` now
implements W1's existing `BlobManifestReader` for filesystem and S3. Supply it
to `Tenant.BlobRecords` in `BindData`. It verifies the selected tenant's full CAS
object and closes it before returning members, without opening a nested store
transaction. Physical reads grant no receipt or reference edge. Real SQLite
boundaries cover forged metadata, unrelated same-run receipts, independent
uploads and reopening. Controller authentication remains W2's integration.

**W1 C1 record layout integration.** Output and manifest writers use the run ID
as `Name` with an empty `Scope.Key`, as C1 declares. `ResultBlobKey` still uses
`Name=result` and a run/attempt/generation key. The W7 physical/store fixture
passes using that W1 key. A temporary overlay with the C1 key reproduces
`result key differs from attempt fence`. Memo writers/readers use
`MemoRecordName=result` with optional manifest/members. W1 still writes and
looks up `output` and `manifest`. Align admission, copy destinations and the
output-size check with C1. The repo-bound remote client also leaves tenant and
verified-default-ref selection to the server, while W1's exact lookup still
requires those fields. W7's component readers and physical adapters are ready.

**W3 post-merge storage layers.** W7 resolved the protocol conflicts and migrated
its memo/artifact tests to JobSpec, Artifacts and Memoized. The retained
`TestHostedRunEmitsTheDispatchersEvents/local` now compiles but fails with
`no such table: sparkwing_meta` through the legacy state path. Migrate that test
with the final state owner. The cache layer now consumes the existing storage
callback before restore and refreshes it after execution before save. Connect
that callback to each fresh StorageBinding when constructing the final hosts.
Generated tests cover changed attempt/generation and refused bindings without
falling back to an ambient local archive. Final host construction remains W3/W8.

## From W4 review fixes (2026-10-15)


**DESIGN: W3 production service router (finding 2).** Invariant: the SDK and negotiated production service router implement one route/error contract. Replace the header/426 gate, `/nodes` routes, legacy errors and missing service handlers together. Test SDK calls against the actual registered node-host router for secrets, output, mask, annotation, summary, OIDC and children. Removing the version gate alone is insufficient.

**DESIGN: W3/W8/W9 execution identity construction (finding 3).** Invariant: every launched process receives the verified selected engine and server-granted runner identity. Carry both through one execution-input constructor used by worker, claimed execution and pod paths. Test real constructors through hello; forged ambient values must never select identity. Amendment A1-19 already assigns this work.

**DESIGN: W3 eval consumption (finding 4).** Invariant: a closure failure cannot become a successful value. Centralize host eval decoding so it checks the canonical error before exposing a typed value. Reproduce throwing step guards, generators and memo keys returning `{value:null,error:...}`; assert failed work, no body execution, no accepted expansion and no NoMemo success. Shape-only tests do not prove this invariant.

**DESIGN: W3 expansion acceptance (finding 5).** Invariant: original generated bytes cross strict expansion validation before any typed reconstruction or persistence. Carry raw job JSON to `ValidatePlan` with source, existing topology, secrets and plan concurrency. Test unknown/duplicate fields, null collections, 256/257 fan-out, cross-boundary cycles and mismatched concurrency through the real scheduler acceptance path. Re-marshalling permissively decoded structs cannot preserve evidence.

**DESIGN: W3/W5 terminal-state delivery (finding 6).** Invariant: terminal state and attempt boundaries reach SDK predicates and bodies before invocation. Consume the explicit settlement amendment above. Production `internal/nodehost/steps.go` still omits the new state on both body and predicate requests. The verified Go process tests use the new contract; production execution remains blocked until host and TypeScript parity land.

**DESIGN: W3/W9 pipeline identity resolution (finding 7).** Invariant: invoked project name and registered SDK entrypoint remain distinct through discovery, parsing and execution. Resolve the configured pair once and carry it through CLI argument parsing and host planning. Test project YAML alias `deploy-prod` targeting `Deploy`, with required pipeline/job flags and the invoked alias visible to author code. The SDK-only test already receives a resolved pair and cannot prove this mapping.

**DESIGN: W3 environment projection (finding 9).** Invariant: automatic startup classification and complete cleanup environment come from one procenv projection. Use `SplitJobEnv`/the shared builder before process creation and supply cleanup with the complete attempt environment. Test unmarked GOMAXPROCS/NODE_OPTIONS at interpreter startup, unchanged declarations on replan, and body/cleanup using the same DOCKER_HOST. An explicitly marked startup variable cannot establish automatic classification.

W3 also owns finding 1's retained test port. `go test -p 4 -gcflags=-e ./internal/nodehost -run '^TestHostedRunEmitsTheDispatchersEvents$' -count=1` fails compilation. `events_test.go` still references PlanDocOld, PlanModifiers, PlanNode, PlanConsume, PlanApproval, PlanClosures and SkippedConcurrent. Its auto retry branches and node_auto_retry expectations describe removed behavior. Other node-host test files fail on old APIs too. The two orchestrator RetryAuto tests still pass removed `auto=true` to the fixture and need porting to retained retry semantics.


**W3: `internal/nodehost` tests fail after the W4 protocol merge (c30046f22).** `go test ./internal/nodehost/` fails TestAdmissionLeasesHostAroundEachNode, TestAdmissionPriorityAndClassPrecedence and TestAdmissionRunHoldingTheHostTakesOnlySemaphores, then hangs until the 10-minute test timeout. The W4 review (review/areas/W4-review-3.md, findings 2, 4, 5, 9) names node-host gaps against protocol v2: `routes.go` still installs the old version-header/426 gate and `/nodes` routes, eval results ignore `EvalResult.Error`, generated declarations skip strict expansion validation, and startup/cleanup environments bypass procenv. Fix those, and make no test able to hang: a test that waits must fail on a deadline.

**DESIGN: W3 concurrency identity codec (from W4 review-3 fixes).** Invariant: encoding a validated `(scope, host identity, group)` tuple is injective, and its label recovers the whole host identity. `ConcurrencyKey` joins fields with colons while `ScopeLabelFromKey` splits at the first colon, so `(a, :b)` and `(a:, b)` both encode as `run:a::b`, and labels lose `:with@seps`. Own one tuple codec, with unambiguous field encoding and a single decoder, used by admission and every label reader; do not add another separator special case. W4 restored the failing tests on its branch: `go test -p 4 ./internal/orchestrator -run '^(TestScopeKey|FuzzConcurrencyKeyPreservesIdentity)' -count=1`.

**W1 scope clarification (lead).** When W1 removes a legacy store API (Store.AcceptPlan and its validator, StateStoreOld, old claim and trigger writers, offer rounds, old maintenance APIs), W1 migrates or deletes every remaining caller, including callers in W1b-owned test fixtures such as the output and log fixtures reached through dispatchRun. Keep each caller change minimal and list the files in your report. This overrides the "do not edit W1b's files" line for caller migration only. W1b does not reintroduce removed APIs.

**W1: separate cancellation intent for W2.** W2's cancellation-reason persistence test fails because the store has no cancellation-intent field separate from the terminal reason. Add it to the store's authentication/cancellation snapshot (amendment A1-21), and finish the session digest and CSRF persistence W2 reads. W2 consumes it through the contract interface.

W1 continuation status: cancellation reasons now persist independently on runs and
jobs and reach job, owner and eval renewal. Session digest lookup, nanosecond
writes, current membership, capability-parent ceilings and sealed CSRF-key
persistence are implemented. `CreateTokenSession` is removed. Session creation and
lookup no longer accept caller clocks. See `../w1-session11-progress.md` for caller
migrations and evidence. W2 still needs to extend C2 `BindingState` with the
cancellation reason before W1 can project it through `ReadAuthentication`. W1 has
not changed that foreign contract. OAuth/browser middleware and roleless-session
representation remain integration points.

W1's unit-reaper cut is complete. The unfenced node, agent-loss and trigger
reapers and dispatcher-expiry helpers are removed. Remaining callers use the
leased `ReapUnits` contract. The elected controller pass preserves remote runs,
and the SQLite local-orphan cell proves unstarted requeue and started failure
through reopening. See `../w1-session13-progress.md` for migrated callers, ports
and verification. W1b still owns settlement wiring. Its unused `expiredClaim`
and `settleExpiredClaimTx` helper can retire after this cut is merged.

**DESIGN approved (lead, pending Korey's ratification): SDK planning diagnostic sanitization, W5 and W4.** Invariant: no declared secret argument value (default or supplied, pipeline or job scope) appears in any error the SDK returns from describe, argument resolution or planning: not in message, hint, details or detail keys, in any encoding. Each SDK collects the nonempty declared secret values once at its discovery/planning boundary and passes every outgoing canonical error through one sanitizer, which keeps codes and field paths. That replaces the describe-only replacement. Host masking still owns execution output and dynamic service values. Both SDKs carry a property test over thrown values, canonical errors, overlapping values, encodings and both argument scopes. W5 implements it in TypeScript; W4 implements the same in the Go SDK. No cross-area contract change.

**Contract change needed, W1/W9/W3: attempt identity on job transitions.**
Invariant: an execution report affects only the attempt it names, and replay of
that report cannot consume another attempt. C1 `NodeTransition` and C3
`JobTransitionRequest` carry claim generation but no source attempt ordinal.
Two successive identical retry failures therefore hash identically. A direct
store probe with retry budget two starts attempt four, then the next failure
returns `Replayed:true, Attempt:4` instead of starting five. A delayed report
also lacks an attempt fence after retry under the same claim. Carry the source
attempt through the request and adapter, check it in the committing transaction,
and include it in replay identity. Start, unstarted refusal and reaper semantics
must remain explicit. W1 has not changed the foreign wire shape. The reproducer
and command are in `../w1-session12-progress.md`.

**DESIGN: W9 Windows release packaging for the TypeScript SDK (from W5 review finding 6).** Invariant: every published TypeScript SDK package contains working protocol bridges for win32-x64 and win32-arm64. The release workflow builds both `native/win32-*/protocol.node` artifacts on native Windows runners, runs TestNativeProtocol and TestNativeConformance on each, stamps the SDK version, and makes publication depend on package assembly. W5's proposal is in `sdk/typescript/IMPLEMENTATION.md`.

**DESIGN approved (lead): W1b record mutation authorization.** Invariant: no record, manifest or memo copy persists unless the writer, ancestor and holder authority is current at the moment of the write. Record publication has one transaction-local mutation owner used by both CommitRecord and CopyMemo: prepare namespaces and verified members first (any I/O happens here), then take the existing blob locks, recheck current authority against the store clock inside the same transaction, and persist the complete destination batch through that owner. That final recheck is the linearization boundary; nothing writes after it without passing it. Preserve immutable replay, all-or-nothing output and manifest copies, and caller-owned rollback. Remove the scattered per-path checks this replaces. TestBlobRecordsMutationCannotOutliveAuthority must go green with its full generated-case run.

**W1b may make two catalog edits in W1 files (lead).** W1b deletes `output_runs` and `output_tombstones` from `tenantTables` in `pkg/store/tenant.go`, and the `(*Store).ExpiredOutputRuns` entry from `reviewedUnscopedSQL` in `pkg/store/tenant_sql_scope_guard_test.go`. These are exactly the two edits listed in W1b-HANDOFF.md; nothing else in W1's files.

**W1b priority (lead).** After the two items above, prioritize legacy cutovers and real integration over more test coverage: retire the legacy claim-credit, raw storage, output, direct-upload and log paths you own, migrating callers with the smallest change (the W1 scope note above applies to you in the other direction for caller migration only), and join W1's real issuance/start/finish transactions to the billing helpers (A1-27).

**DESIGN approved (lead): W8 pending-claim outcomes.** Invariant: a committed award is always owned, and every independent stop reason (policy refusal, cancellation, idle, claim deadline, version) stays observable to the caller. The pending claim returns the award and the stop reason as independent results. Run adopts any returned award first, then applies the stop reason, joins the held hosts, and returns that reason; the joining defer never replaces a stop reason with success. Existing owners of cancellation, idle, claim-deadline and version checks stay as they are. `TestLoopPendingAwardPreservesPolicyDenial` must go green, plus a property test over interleavings of award delivery and each stop reason.
