# W1b handoff

Continues `15b61d48b`. W1b remains incomplete. These changes are uncommitted.
The lead owns review, committing, delivery pipelines and native integration.
No Git writes, Sparkwing binary runs or W1 implementation edits were made.

## Implemented

Published job logs now use the live run-bound publisher with `data.write` and
an exact terminal imported attempt in a publish-mode run. The existing blob
authority owner rechecks ancestry, expiry, tenant, repository and run scope.
Publication needs no execution lease or final-attempt grace because the local
attempt has already ended. Segment integrity, contiguity, retention and immutable
sealing still use the existing log implementation. Incomplete logs leave the
imported job result unchanged. Final replay rechecks live publisher authority.

The new `storage-publish-one-way/store-data-sqlite` cell commits completed local
job logs, reopens and reads them, checks unique retained bytes, immutable replay,
revocation and unchanged results, and confirms no executable unit or billing
interval was created. Authority and imported results are preseeded contract
facts. This proves the persistence portion, not the complete publication flow.

The SQL guard now names the existing payment identity resolvers and operator
storage reports. It still requires team predicates for tenant operations.
The writer guard recognizes the existing final storage writers.

## Deletion and ports

Removed nine obsolete SQL exemptions (**24 lines**) and three obsolete writer
backlog entries (**3 lines**). Operator report exemptions retain their reasons
under their current receivers. No legacy production API was removed this session
(**0 production lines**). Remaining legacy APIs still have runtime callers.
No archived inventory row is newly claimed ported or closed.

## Unit

The store selection covers billing, payment events/refunds, blob records,
receipts, uploads, quota settlement, logs, metadata and retained storage.
Two new log tests cover a completed imported attempt and eleven authority/identity
refusals. The refusal cases include executable runs, missing imported attempts,
changed generations, open attempts, revoked ancestry, expiry, foreign repository
or run, missing grant, claim-only data authority and owner-unit subjects.

Nine SQLite persistence cells now pass:

- `billing-ledger-survives-retention/store-data-sqlite`
- `storage-replica-safe-accounting/store-data-sqlite`
- `storage-quota-settlement-crash-replay/store-data-sqlite`
- `storage-log-segments-and-fallback/store-data-sqlite`
- `storage-hasmany-read-set/store-data-sqlite`
- `storage-reachability-and-retention-tails/store-data-sqlite`
- `storage-memo-and-depcache-scope/store-data-sqlite`
- `storage-reference-edge-laundering/store-data-sqlite`
- `storage-publish-one-way/store-data-sqlite` (new)

The new cell and positive unit test failed before the implementation with
`job log grant is required`. Fixtures were corrected for final run status,
capability identity uniqueness, exact reader projections and required run fields.

## Manual

Commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
and GOCACHE/GOTMPDIR/TMPDIR beneath `/tmp/sw-astra-W1b/`. Run with
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`.

- Whole repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/... ./tests/acceptance/...`
  passed.
- Store selection pattern:
  `^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling)`.
  It passed with **128 top-level tests / 282 passes including subtests** and no skips.
- `go test -p 4 ./internal/acceptance/... ./tests/acceptance/... -count=1`
  passed with **20 top-level tests / 202 passes including subtests** and
  **2,995 existing native/platform skips**.
- Both `TestTenantWriters` tests passed. The SQL scope guard remains red on
  **10 inherited W1 findings**, down from 28 before this session. The remaining
  findings concern authority/award resolution, retention queries and three stale
  retention exemptions. No W1b SQL finding remains.
- `go test -p 4 ./pkg/docs -run '^(TestPkgDocsContentMatchesDocsRoot|TestEmbeddedChangelogMatchesRoot)$' -count=1`
  passed. Commentcheck, sleepcheck and `git diff --check` passed.
- No local Git identity or core.worktree override was found.

## Not tested or failing

The full `pkg/store` suite ran before edits and failed in **31 top-level tests**.
These include schema classification/writer/scope guards and old token,
execution-policy, concurrency, claim and database-path tests. No test in the
existing W1b-focused selection failed in that baseline. The full suite was not
rerun after the scoped guard cleanup. The separate teststore package's earlier
sparkwing_meta failure remains inherited evidence.

No PostgreSQL, race, provider, native placement, daemon, candidate, pipeline or
release check was run. The brief assigns pipelines and independent review to
the lead because this sandbox cannot open sockets.

## Contract changes needed and integrations remaining

1. W1/W2 need to declare how published trigger/plan/eval log identities are
   imported without executable units. Current log contracts name a live unit
   lease, while publish intake has none. Job logs are implemented against the
   existing terminal attempt contract. Owner-unit publication remains refused
   until its imported identity is defined. No foreign contract was changed.
2. W1 must join immutable awards, interval start/finish, periodic charges and
   final settlement to issuance, starts, waits, renewal, release, finish and
   reaping. Memo materialization must join result state atomically. Published
   run/plan/result import must precede W1b job-log and blob publication.
3. W2 must implement the billing calculator and verified payment handoff.
   Test/live identity for retained cards, attempts, warnings, freezes and refunds
   remains a cross-area concern. Data authorization and signing are still stubbed
   here. Store tests use contract fakes where those owners are unavailable.
4. W7 must implement namespace/edge policy, verified manifest reads, physical
   verification/deletion and trusted replica identity. W2/W7/W9 must wire final
   publication, segment transfer, retention, egress and ring fallback. Data callers
   must supply complete published-attempt selectors to CheckDataAccess.
5. W1/W9 need the final custom-event append/sequence contract before
   AppendEventCharged and chargeStorageTx can be removed. The charged route
   returns a sequence and has different event-limit behavior from raw append.
6. W1 owns removal of the unused egress_day table, classification and operator
   deletion-list entry. Its implementation files remain untouched.
7. Legacy claim credits, paid grants, raw storage counters, direct uploads,
   output persistence and node logs retain W1/W2/W7/W9 callers. In particular,
   controller log_segments.go, log_reads.go and storage_pass.go still use old
   node-log persistence. Physical age-based cache listing remains in that pass.
   W7/W9 must retire StateStoreOld manifest consumers.
8. Remaining tenant-SQL and table-classification cleanup belongs to W1.

No human decision is requested. No setting, schema, limit or exported interface
was added. The local C1 notes document enforcement of existing publication
contracts and identify the missing owner-unit import contract.

## Shared files and friction

Outside pkg/store: `internal/acceptance/store_data.go`, `store_logs.go` and new
`store_published_logs.go` add the persistence proof and share its historical
reader fixture. `design/rewrite/contracts/c1-data.md`, `CHANGELOG.md` and generated
`pkg/docs/changelog.md` document the behavior. No runtime caller migration was
needed. Shared store guards only change W1b exemptions and writer entries.

Friction: published log commits previously required a job claim. Legacy schemas
and guard classifications still name removed tables, while runtime callers still
use some legacy APIs. Missing foreign implementations remain integration work.
Scratch is deleted after final verification. No owned process remains.

STATUS: CONTINUE
