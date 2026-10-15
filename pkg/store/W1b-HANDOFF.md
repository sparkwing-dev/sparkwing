# W1b handoff

Continues 4b70b8c14. W1b remains incomplete. The lead owns commits,
independent review, pipelines and native integration.

## Implemented and deleted

Removed Store.ReserveStorage, Store.CommitStorage, StorageReserve,
StorageReservation, StorageCommit and DefaultStorageReservationTTL. There are
no aliases. Public uploads use Tenant.ReserveBlobUpload, CommitBlobUpload and
AbortBlobUpload. Generic caller-selected byte deltas cannot create final blobs
or receipts.

The sole production ReserveStorage caller was the controller's log-retention
probe. It now reads Tenant.StorageStanding and available slots. The probe
preserves funded/free/available-slot/full-slot classification without creating
or releasing a reservation. CommitStorage had no production callers.

The exact-size legacy upload transaction keeps private request/result models.
Its unused partial-size mode, tier/grant response fields and JSON tags are gone.
Direct-upload and node-log callsites name those private models. The internal
TTL fallback stays one hour, and direct uploads still select their existing TTL.
The unused nodeLogColumnsOf helper is removed.

Production Go removes 109 lines and adds 52, a net reduction of 57. Legacy test
retirement removes 517 lines and retains 33 shared helper lines in
legacy_storage_helpers_test.go, a net reduction of 484 test lines. The total
Go reduction is 541 lines. No W1-owned lifecycle, schema or catalog file changed.

## Test ports and policy question

Existing upload_runtime_test.go and upload_quota_runtime_test.go cover the
surviving guarantees of the removed raw byte-counter tests: reserve/abort
release, immutable verified size/digest, wrong tenant/uploader refusal,
concurrent admission and commit, receipt replay, expiry, deduplication and
reopening. Those proofs run through final tenant tickets. Shrinking overwrites
and partial-size grants are retired mechanisms. No already-complete acceptance
cell was added, and no archived T1 source row is newly marked complete.

The four retired first-byte/free-slot tests are not claimed as final-ticket
coverage. A generated two-connection probe demonstrated that final blob tickets
do not acquire free slots. The first proposed gate was removed because legacy
small outputs can bypass a full slot pool. Final upload tickets have no record
kind before signing, so a blanket slot gate would remove that exception.
No slot gate or new storage restriction remains in this diff.

Contract clarification needed from the lead/C1/W7: decide how final ticket
admission preserves the small-output exception before enforcing slots there.
A server-verified intended-use distinction would require changing the foreign
blob upload contract. Alternatively, removing the exception needs an explicit
product decision. An unverified caller boolean is insufficient. The existing
raw direct-upload small-output exception remains intact.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR are beneath /tmp/sw-astra-W1b/.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for store, controller and both acceptance packages.
- The final store selection passes 183 top-level tests / 594 cases without skips.
- Five controller egress/event regressions pass.
- TestStorage and TestBilling pass 43 cases, including all 12 existing SQLite
  storage/billing proofs. There are 562 native/platform skips. No new native
  cell is unskipped.
- commentcheck -base main and sleepcheck -base HEAD pass.
- gofmt, git diff --check, sync-docs.sh --check and check-changelog.sh pass.
- The regenerated store API snapshot matches. Whole-directory comparison still
  finds the inherited pkg_runner.txt snapshot for a removed package.

Store selection:
^(TestBlob|TestLogPersistence|TestLogPublication|TestLogMaintenance|TestStoreData|TestRetainedStorage|TestPaymentSettlement|TestMeteredAccounting|TestTenantSQLScope|TestTenantWriters|TestTenantTwins|TestGrantCredits|TestCreditGrantRequest|TestCreditsBalance|TestCreditReversal)

Controller selection: ^(TestControllerEgress.*|TestControllerEventsUseTenantAndReplica)$.
Acceptance selection: ^(TestStorage|TestBilling)$ on both acceptance packages.

The temporary slot probe was removed with its rejected gate. Its initial
failure and subsequent passing gate experiment establish the policy conflict,
not a shipped proof. A vet warning in that discarded fixture was a Store mutex
copy. The retained code passes vet without it.

No full legacy store/controller suite, PostgreSQL, race, provider, physical
backend, log transport or native-placement pass is claimed. The old-schema
fixture failures recorded by predecessors were not rerun or waived as passes.

## Assignment audit and real integration

- The approved record-mutation owner and two output catalog edits remain done.
  Legacy raw API retirement progressed by the six exported removals above.
- A1-24 remains implemented at the store boundary. W2 DataAuthority and BindData
  are still panic stubs in this checkout.
- A1-27 remains open here. Read-only inspection confirms W1's parallel worktree
  has unit_billing.go and the lifecycle join. Neither is merged into this
  branch. This checkout still refuses metered issuance in authority_issue.go.
  W1 files were not copied or reimplemented. Its pending price readers must use
  AsOperator(), and accountUnitBillingTx can drop its duplicate unmetered
  calculator override after joining the shared fix. C2 SettleBilling remains
  a panic stub. W3 must report actual owner compute to exclude customer waits.
  Legacy claim-credit retirement and spend/grace enforcement remain W1b work.
- A1-28's committed-checkout settlement and raw paid-grant refusal remain done.
  W2 owns verified provider events and the HTTP adapter.
- A1-29 and A1-31 through A1-35 retain implemented store contracts for tagged
  uploads, receipts, records/members, memo/depcache, read sets, tombstones and
  tagged logs. W7 RecordNamespace and AuthorizeEdge remain stubs here. Physical
  verification/deletion, manifest-reader setup, signed transfers, binary policy,
  proxy egress and allowance-driven physical eviction remain integration work.
- Raw ReserveUpload/CommitUpload, output objects, ReleaseStorage and node-log
  transports still need consumer cuts. Capability transport and imported owner
  log identity remain open. The old output-key retention sweep stays removed.
- A1-53 remains with the lead and integrating areas for native/provider/
  PostgreSQL/physical-backend evidence.

## Shared files and friction

The only shared production change is pkg/controller/log_segments.go, migrating
its storage probe. W1-owned files are untouched. Shared prose/generated files:
contracts/c1-data.md, handoffs.md, t1-port-inventory.md, CHANGELOG.md,
docs/migrations/_unreleased.md, .apidiff/pkg_store.txt, pkg/docs/changelog.md and
its migration mirror. STORE-DATA.md records the persistence surface.

The missing W1 merge and C2/W7 stubs prevent the remaining real joins in this
checkout. The stale runner snapshot prevents a clean full API comparison.
The slot/small-output distinction above is the new contract question.

The shared Git config has no local identity or core.worktree override. No Git
write, Sparkwing binary, candidate, pipeline, service, deployment, release or
harness-memory write ran. Scratch is deleted after the final checks.
