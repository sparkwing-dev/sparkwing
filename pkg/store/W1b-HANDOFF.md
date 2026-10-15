# W1b handoff

Continues `3ac9d0a02`. W1b remains incomplete. Changes are uncommitted.
The lead owns review, committing, delivery pipelines and native integration.
No Git writes or edits to W1 implementation files were made.

## Implemented

- Persisted memo read edges now allow own scope and verified default fallback.
  Depcache also allows the reader's sealed, verified PR base. A general read-ref
  grant alone cannot authorize a fallback. Historical default evidence for a
  different branch cannot substitute for the reader's sealed default branch.
- The shared calculation governs HasMany, data download checks and reference
  commits. It closes the broader read-set path left open after the prior
  session fixed LookupMemo and LookupDepcache. No new setting, interface,
  schema, limit or customer policy was added.
- CheckDataAccess carries a complete target run/job/attempt/generation tuple
  into the existing authority recheck. Publication can select an imported
  attempt, and its receipt remains confined to that attempt. Foreign runs,
  nonexistent attempts/generations, unrelated receipts and absent selectors
  cannot borrow its read or reference authority. Uploading for another imported
  job remains allowed under the publisher's existing scope.
- Added `storage-memo-and-depcache-scope/store-data-sqlite`. It reopens the
  store and exercises HasMany plus download/record authorization across own,
  PR-base, former-default and unproven/default cases. Records and publication
  authority are preseeded; namespace policy, App intake and physical bytes are
  outside this boundary proof.

## Deletion and ports

Deleted **0 legacy runtime API lines**. Replaced one overly broad fallback
predicate and threaded existing selectors through their declared contract.
No API or outside-package caller was removed or migrated.

Extended the store-boundary port of archived
`TestCacheRefs_FollowTheGrantsSignedRefs`: permitted depcache fallback cannot
broaden memo access through another API. Added publication receipt-selector
regressions. No complete archived inventory row or native placement is newly
claimed closed.

## Unit

- Store selection: **123 top-level tests, 259 passes including subtests**, no
  skips. Pattern:
  `^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling)`.
- Acceptance packages: **20 top-level tests, 200 passes including subtests**,
  with **2,995 existing native/platform skips**.
- Ten cache cases check presence, download authorization, reference checks and
  actual record commits without an uploader receipt masking the result.
- Twenty-four publication cases check upload, commit, download and record
  authority with own, unrelated, foreign, absent and stale attempt selectors.
- Before the fixes, tests failed on the intended unauthorized cache reads and
  refused valid publication requests. Go overlays restoring each former rule
  reproduce those failures without mutating production files. The cache
  acceptance cell also fails with the old read-set rule.

Seven SQLite persistence cells pass:

- `billing-ledger-survives-retention/store-data-sqlite`
- `storage-replica-safe-accounting/store-data-sqlite`
- `storage-quota-settlement-crash-replay/store-data-sqlite`
- `storage-log-segments-and-fallback/store-data-sqlite`
- `storage-hasmany-read-set/store-data-sqlite`
- `storage-reachability-and-retention-tails/store-data-sqlite`
- `storage-memo-and-depcache-scope/store-data-sqlite` (new)

## Manual

Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
and GOCACHE/GOTMPDIR/TMPDIR beneath `/tmp/sw-astra-W1b/`. Run with
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`.

- Whole repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/... ./tests/acceptance/...`
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

The complete legacy store suite is not green. A fail-fast run of
`go test -p 4 ./pkg/store/... -failfast -count=1` fails in
TestRecordProfileObservation_IgnoresLegacySamples because pipeline_profiles.team
is required, and in ExampleOpen and teststore.TestOpenKeepsStateSeparate because
sparkwing_meta is removed. These inherited fixtures and APIs remain outside
this change. No PostgreSQL, race, provider, native placement, daemon, candidate,
pipeline or release check was run. Independent review remains with the lead.

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
   AppendEventCharged and chargeStorageTx can be removed. The existing charged
   route returns a sequence and has different event-limit behavior from raw
   append. No competing contract was introduced.
5. W1 owns removal of the unused egress_day table, classification and operator
   deletion-list entry. Its shared implementation files remain untouched.
6. Legacy claim credits, paid grants, raw storage counters, direct uploads and
   node logs still have W1/W2/W7/W9 callers. Elected maintenance, allowance and
   nonpayment eviction, receipt pruning, log lifecycle/egress and ring fallback
   need their runtime integrations.

No new cross-area contract change or human decision is requested. C1 source
notes describe implementation of existing contracts.

## Shared files and friction

Shared files changed: internal/acceptance/store_data.go, new
internal/acceptance/store_cache_scope.go, design/rewrite/contracts/c1-data.md,
CHANGELOG.md and pkg/docs/changelog.md. Store files changed:
blob_read_authority.go, blob_restore_test.go, blob_receipts_test.go and this
handoff. No outside-package API callers changed.

Friction: indexed ref_scope must change with the capability envelope in trusted
fixtures; the store correctly refused their initial mismatch. Publication's
attempt must travel through DataTarget into the recheck. Standalone commentcheck
and sleepcheck binaries are absent; their repository Go entry points work.
Missing foreign implementations remain integration work, not passing native
acceptance. Scratch is removed after checks; no owned process remains.

STATUS: CONTINUE
