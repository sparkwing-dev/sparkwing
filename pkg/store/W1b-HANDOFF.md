# W1b handoff

Continues `33ca59a2c`. W1b remains incomplete. These changes are uncommitted.
The lead owns review, committing, delivery pipelines and native integration.
No Git writes or edits to W1 implementation files were made.

## Implemented

- Persisted job-log reads recheck the recorded writer and current attempt fence
  within the final-attempt grace. They cannot reuse the exception for identical
  sealed writes to read indefinitely. Historical reads still work under live
  unbound run/log read authority. Identical final write replay still works.
- Trigger-log reads recheck the owner lease even though trigger awards carry
  `runs.read`. Expired leases, changed generations, other holders, other
  capability prefixes and ended units refuse the read. A live trigger retains
  reads of both owner-unit and job-attempt subjects in its run. This uses the existing
  owner authority function. No new setting, interface, schema or limit was added.
- Added `storage-reference-edge-laundering/store-data-sqlite`. After reopening,
  presence and download/reference authorization admit output, manifest and member
  digests through direct, optional, transitive, artifact-input, generated-group
  and failure-handler dependencies. Unrelated, foreign-team and missing digests
  stay hidden. Metadata checks consume no download or egress usage. Authority and
  the accepted graph are preseeded. Intake, physical bytes and record commits are
  outside this cell's proof.
- Extended the log SQLite cell with terminal-writer refusal, historical read
  authority and identical final-write replay.

## Deletion and ports

Removed `Store.PruneExpiredCacheObjects` and its controller call. This deletes
**18 production Go lines**: 13 in the store and 5 in the caller. Removed **27
obsolete test/guard lines**, including the old default-tenant age-sweep test.
Record expiry and tombstone GC already own final blob lifetime.

Ported `TestPruneExpiredCacheObjectsIncludesDefaultTeam` to
`TestBlobGCReferenceProtectsOldBytesAcrossTeams`. It checks default/a/b tenant
collection, protects referenced bytes older than the old cache window, and
collects them only after their roots disappear and the tombstone grace passes.
The old output-exclusion assertion is covered by the existing pinned-output
retention cell.

The new read-set cell extends the store portion of the reversed
`TestDataDownloadSharesCommittedArtifactsWithinTheRepository` and
`TestDataDownloadSharesCommittedArtifactsWithinTheTeam` ports, plus the
unrelated-job finding in the port inventory. It does not close their native
route, physical backend or full materialization obligations. No complete
archived inventory row is newly claimed closed.

## Unit

- Store selection: **126 top-level tests, 269 passes including subtests**, no
  skips. Pattern:
  `^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling)`.
- Acceptance packages: **20 top-level tests, 201 passes including subtests**,
  with **2,995 existing native/platform skips**.
- Before the log fix, both terminal-log cases and all five stale-trigger cases
  failed on unauthorized successful reads. The new log acceptance assertion
  also fails when a Go overlay restores the former read implementation.
- A negative-control overlay allowing same-run reads without dependencies makes
  the new reference cell fail on unrelated output, manifest and member digests.
- Fixture corrections added the default team for cross-team GC and the historical
  job attempt required by the log stream's foreign key. Both initial failures
  came from missing fixture rows.

Eight SQLite persistence cells pass:

- `billing-ledger-survives-retention/store-data-sqlite`
- `storage-replica-safe-accounting/store-data-sqlite`
- `storage-quota-settlement-crash-replay/store-data-sqlite`
- `storage-log-segments-and-fallback/store-data-sqlite`
- `storage-hasmany-read-set/store-data-sqlite`
- `storage-reachability-and-retention-tails/store-data-sqlite`
- `storage-memo-and-depcache-scope/store-data-sqlite`
- `storage-reference-edge-laundering/store-data-sqlite` (new)

## Manual

Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
and GOCACHE/GOTMPDIR/TMPDIR beneath `/tmp/sw-astra-W1b/`. Run with
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`.

- Whole repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/... ./tests/acceptance/... ./pkg/controller/...`
  passed.
- `go test -p 4 ./pkg/store -run '<Unit pattern>' -count=1` passed.
- `go test -p 4 ./internal/acceptance/... ./tests/acceptance/... -count=1`
  passed.
- `go test -p 4 ./pkg/docs -run '^(TestPkgDocsContentMatchesDocsRoot|TestEmbeddedChangelogMatchesRoot)$' -count=1`
  passed.
- `go run -p 4 ./internal/commentcheck -base HEAD <worktree>`, the equivalent
  sleepcheck, `bash <worktree>/bin/sync-docs.sh --check` and `git diff --check`
  passed.
- No local Git identity or core.worktree override was found.

## Not tested or failing

`go test -p 4 ./pkg/store -run '^TestTenantSQLScope' -count=1` fails on **28
existing guard findings**. They include unscoped payment identity resolution,
operator storage reports, W1 authority/retention queries and stale exemptions
for removed APIs. An overlay restoring the previous guard reproduces every
finding and adds the retired method's stale exemption. There are no new
findings from this change. The guard reads source files from disk, so Go overlays
do not redirect its SQL scan.

The full legacy store suite was not rerun. The prior handoff records failures
in TestRecordProfileObservation_IgnoresLegacySamples on pipeline_profiles.team
and in ExampleOpen and teststore.TestOpenKeepsStateSeparate on sparkwing_meta.
No PostgreSQL, race, provider, native placement, daemon, candidate, pipeline or
release check was run. Independent review remains with the lead.

## Contract and integration work remaining

1. W1 must join immutable awards, interval start/finish, periodic charges and
   final settlement to issuance, starts, waits, renewal, release, finish and
   reaping. Memo materialization must join result state atomically.
2. W2 must implement the billing calculator and verified payment handoff.
   Test/live identity for retained cards, attempts, warnings, freezes and refunds
   remains a cross-area concern.
3. W7 must implement namespace/edge policy, verified manifest reads, physical
   verification/deletion and trusted replica identity. W2's data controller is
   still stubbed here. Its callers must supply complete published-attempt
   selectors to CheckDataAccess. W7/W9 must wire final publication and retire
   StateStoreOld manifest consumers.
4. W1/W9 still need the final custom-event append/sequence contract before
   AppendEventCharged and chargeStorageTx can be removed. The charged route
   returns a sequence and has different event-limit behavior from raw append.
5. W1 owns removal of the unused egress_day table, classification and operator
   deletion-list entry. Its shared implementation files remain untouched.
6. Legacy claim credits, paid grants, raw storage counters, direct uploads and
   node logs retain W1/W2/W7/W9 callers. Elected maintenance, allowance and
   nonpayment eviction, receipt pruning, log lifecycle/egress and ring fallback
   need their runtime integrations. Physical age-based cache listing remains in
   the old controller storage pass and needs W7's final-backend cut.
7. The tenant-SQL guard needs coordinated cleanup across both store workers.

No new cross-area contract change or human decision is requested. C1 source
notes describe enforcement of existing authority and retention contracts.
Mechanisms added: none.

## Shared files and friction

Outside pkg/store: `pkg/controller/storage_pass.go` removes the one caller.
`internal/acceptance/store_data.go`, `internal/acceptance/store_logs.go` and new
`internal/acceptance/store_reference_edges.go` supply the boundary proofs.
`design/rewrite/contracts/c1-data.md`, `CHANGELOG.md` and generated
`pkg/docs/changelog.md` describe the behavior. Inside pkg/store, the shared guard
only loses the retired method's exemption. The remaining changed files belong
to log persistence, blob GC, direct upload and output retention tests.

Friction: broad-read grants previously skipped live claim checks. Static guard
exemptions still name removed APIs, and the guard's source parser ignores Go
overlays. Missing foreign implementations remain integration work. Scratch is
removed after verification. No owned process remains.

STATUS: CONTINUE
