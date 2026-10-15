# W1b handoff

Continues 3ca8df53f. W1b remains incomplete. The lead owns commits, independent
review, pipelines and native integration. Earlier continuation reports remain
in Git history and the swarm's rewrite-logs directory.

## This continuation

Completed the first two lead handoffs in contracts/handoffs.md.

Invariant: no record, manifest or memo copy persists unless the writer,
ancestor and holder authority is current at the mutation boundary.

CommitRecord and CopyMemo now share mutateRecordsTx in blob_record_mutation.go.
They prepare every destination namespace and verified manifest before the owner
takes sorted blob locks. The owner checks existence, authorized references and
immutable destinations, then reloads writer ancestry and the holder against the
store clock. Only then does it persist the complete record/member batch and
clear tombstones. The caller retains commit and rollback. Failed commits return
no record or memo hit. Copy deadlines are checked again before commit.
Immutable replay retains the prior record metadata and copied provenance.

Removed 77 lines of the former separate mutation paths and four catalog lines.
No legacy public API or feature was retired in this continuation. No schema,
rate, limit, setting, environment variable or public contract shape was added.
The shared mutation owner is the mechanism the lead explicitly approved.

Deleted exactly the two authorized catalog entries from tenantTables
(output_runs and output_tombstones) and the ExpiredOutputRuns exemption from
reviewedUnscopedSQL. The catalog guards now pass. No other W1 code changed.

The complete pre-existing TestBlobRecordsMutationCannotOutliveAuthority property
failed before the change in all six commit/copy x lease/leaf/ancestor cases. It
now passes its live controls, exact-expiry checks and 48 generated cases per
combination. Existing manifest-I/O expiry, immutable replay, atomic-copy rollback
and copy-versus-GC tests also pass. No archived T1 port is newly closed.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b/. Use
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`. Builds and tests use -p 4.

- Whole-repository go build ./... passed.
- go vet ./pkg/store/... ./internal/acceptance/... ./tests/acceptance/... passed.
- The W1b store selection passed 199 top-level tests / 607 cases with no skips
  or exclusions. It includes the complete publication property and both repaired
  catalog guards. This is not a full-store pass.
- TestStorage and TestBilling passed 2 top-level tests / 43 cases, including all
  12 existing store-data-sqlite proofs. Native/platform skeletons retain 562
  skips. No native cell was activated.
- Required commentcheck -base main passed across 1012 files. sleepcheck -base HEAD
  and git diff --check passed.
- All six generated publication-property branches passed under -race across
  two invocations. The full invocation hit its 240-second test timeout during
  copy/ancestor after the other five branches passed. Running that unfinished
  branch alone passed in 48.636 seconds. No full-invocation race pass is claimed.

The store run selection is:

```text
^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling|TestGrantCredits|TestCreditGrant|TestCreditReversal|TestCreditHistory|TestCreditsBalanceIs|TestCreditsDefaultTenant|TestTenantSQLScope|TestTenantWriters|TestTenantTwins)
```

Use `go test -json -p 4 ./pkg/store -run '<selection>' -count=1 -timeout=240s`.
Acceptance uses `go test -json -p 4 ./internal/acceptance/... ./tests/acceptance/...
-run '^(TestStorage|TestBilling)$' -count=1 -timeout=240s`.
The race invocations add -race and select
`^TestBlobRecordsMutationCannotOutliveAuthority$` (timeout 240s), then
`^TestBlobRecordsMutationCannotOutliveAuthority$/^copy$/^ancestor$`
(timeout 180s). The first invocation timed out as described above.

## Remaining work and contract dependencies

The lead's third handoff is open. Prioritize the legacy hard cuts and lifecycle
billing joins next. Do not restore the deleted reapers or another record writer.

- A1-24 retains the transaction-owned data checker and binding integration.
- A1-27 still needs W1's issuance/start/renew/release/reap/finish joins. The real
  issuer refuses metered roots and otherwise persists unmetered awards directly.
  W1/W2 must agree how an unstarted at_award award refunds its automatically
  opened interval, and how hosts explicitly report customer-runner waits. C1's
  RenewUnit executing flag only records first execution. It does not declare a
  wait/pause operation. Do not silently reinterpret that flag. C2 SettleBilling
  remains a panic stub. The existing accounting tests use a contract calculator
  fake and do not prove real issuance or lifecycle settlement.
- A1-28 retains committed-checkout settlement. W2 owns verified provider events
  and removal of caller-selected paid grants. VerifiedPaymentEvent.Event and
  provider verification remain panic stubs.
- A1-29 through A1-35 retain upload/receipt/provenance/member/reference/depcache,
  tombstone and tagged-log persistence. The approved publication redesign is now
  implemented. W7 RecordNamespace and AuthorizeEdge remain stubs, so store proofs
  use contract fakes. Physical verification/deletion, manifests, GC integration,
  binary execution, trusted replica identity and proxy egress remain open.
- DataController.DataAuthority and BindData remain W2 stubs. The legacy controller
  data_download.go handler still combines source, artifact and binary signing
  through cache grants. Deleting only its binary lookup also removes a transport
  used by existing direct-source callers. The complete old transport and caller
  cut remains open. No endpoint or client was partially disabled here.
- Legacy claim-credit, raw storage counters, output, direct-upload and log paths
  still need retirement. Source identity for never-executed materialization and
  imported owner-log identity still need the previously requested cross-area
  contracts. Result attachment requires the real verified manifest reader.
- A1-53 needs native acceptance evidence. PostgreSQL, providers, physical
  backends and native placements were not exercised here.

No foreign contract changed. These are existing integration gaps, not newly
approved billing semantics. The first two lead entries are done. The third is
not done.

## Files and cleanup

Owned code changes: blob_lookup.go, blob_record_runtime.go and the new
blob_record_mutation.go. Owned documentation: STORE-DATA.md and this handoff.
Shared files: design/rewrite/contracts/c1-data.md, tenant.go (two catalog lines)
and tenant_sql_scope_guard_test.go (the two-line obsolete exemption). No caller
outside pkg/store changed.

No Sparkwing binary, service, provider, deployment, release, Git write or harness
memory write ran. Git identity remains Korey Gambill and core.worktree is unset.
Scratch is removed after verification. No independent review or pipeline pass
is claimed.
