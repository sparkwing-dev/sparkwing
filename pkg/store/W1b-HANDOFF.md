# W1b handoff

This continuation starts at `d795fc6b3`. Earlier metering, payment, blob,
record, upload-quota, policy, reporting and tagged-log implementations remain.
W1b is incomplete. W1's schema, admission, claims, state, reaper, generic quota
and SQL-guard files were not changed. No Git writes were made.

## Changes

- Removed both `SetNodeArtifactManifestCharged` methods. Verified uploads charge
  each new tenant blob once. Logical output and memo references consume no
  additional byte or object budget. A new regression publishes and replays both
  records at the run's one-object ceiling, then refuses a new blob.
- Removed the legacy `artifact-manifest` HTTP handler, registration, tenant-route
  entry, hosted-retry entry and obsolete assertions. The final schema has no
  `nodes.artifact_manifest` column. Reusing the old setter would preserve a
  broken route, so there is no forwarding route or schema compatibility column.
  `TestLegacyManifestEndpointIsGone` invokes the HTTP handler without a socket
  and requires 404.
- Activated `storage-log-segments-and-fallback/store-data-sqlite`. It proves
  immutable segment replay, rollback of an entire gapped batch, persistence
  across closing/reopening, offset reads, incomplete sealing, exact final
  replay, unique retained-byte accounting, subject isolation and revocation.
  A lost log tail leaves the job result unchanged.
- Updated the daemon's two quota route inventory entries from `{principal}` to
  `{team}`. Its route classification check exposed this earlier migration gap.
- Updated the C1 contract note, quota prose, changelog, API reference and OpenAPI
  specification. The API/doc generators made only the expected route deletion.

The log proof uses a real final SQLite store and preseeded capability/segment
metadata. It does not transfer or verify physical bytes. Native placements,
owner-unit transport, ring gaps/stalls, signing, physical expiry and egress
remain integration work. The native scenario cells stay skipped.

## Deletion and ports

Deleted **76 gross production Go lines**: 44 in the store, 29 in the handler and
three route/policy entries. Removed **79 obsolete test lines**, including the
old manifest-per-object accounting assertions and route inventory entries.
The quota inventory correction replaces two additional test lines. Six API
snapshot lines and the removed OpenAPI/reference entry were also deleted.

The old manifest-count expectation is replaced by the final unique-blob budget
regression. Existing final blob tests retain tenant, unrelated-job, member and
read-edge refusal coverage. No additional complete archived T1 inventory row is
claimed closed. No new table, runtime interface, setting or environment variable
was added.

## Unit evidence

- Store selection: **112 top-level tests / 207 including subtests**, no skips.
  Pattern: `^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore)`.
- Acceptance packages: **20 top-level tests / 197 passes**, with **2,995 existing
  skips**. SQLite cells cover replica accounting, upload crash/replay, financial
  retention and now log persistence.
- Controller route checks: `^(TestLegacyManifestEndpointIsGone|TestRouteGuard_|TestTenantRoutes_)` passed.
- Orchestrator checks: `^(TestEveryControllerRouteIsClassified|TestReadRouteListCarriesNoDuplicates|TestHostedRetryPolicy_)` passed after the two quota path repairs.
- A Go overlay disabling the log replay content comparison makes the new cell
  fail with `log replay replaced immutable content`. The production source was
  unchanged by this negative control.

## Manual verification

Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`-p 4`, and `GOCACHE`, `GOTMPDIR`, `TMPDIR` under `/tmp/sw-astra-W1b/`.
Use the assigned worktree with `go -C <worktree>`.

- Whole-repository `go build -p 4 ./...` passed after the endpoint cut.
- `go vet -p 4` passed for `pkg/store/...`, `pkg/controller/...`,
  `internal/acceptance/...`, `tests/acceptance/...`, `internal/orchestrator/...`.
- `commentcheck -base HEAD <worktree>`, `sleepcheck -base HEAD <worktree>`,
  OpenAPI drift checking, embedded-doc sync checking and `git diff --check` passed.
- SQL scope guard retains **28 inherited diagnostics**. Its owned files were
  untouched. The generated Go API snapshot has broad inherited rewrite drift.
  Only the six lines for the two removed methods were cut from its baseline.
- No local Git identity or `core.worktree` override was found.

## Not tested

No native placement, PostgreSQL, daemon, candidate, pipeline, external payment
provider or release was run. This worker cannot run the lead's socket-based
pipelines. Race tests were not repeated because this continuation removes a
legacy path and adds synchronous persistence proofs. Independent landing review
remains with the lead. Scratch is deleted at handoff.

## Contract and integration work remaining

1. W1 must join immutable billing awards and intervals to issuance, starts,
   waits, renewals, release, finish and reaping. Its generic storage quota path
   must resolve effective policy. Memo materialization must remain atomic.
2. W2 must wire billing calculation and verified payment events. Test/live mode
   identity for retained cards, attempts, warnings, freezes and refunds remains
   a cross-area concern.
3. W7/W9 must wire final manifest publication through records/results and the
   physical manifest reader. The old `StateStoreOld` client/host adapters still
   name `SetNodeArtifactManifest`; their full family must be cut when those
   hosts switch contracts. The HTTP endpoint removed here is not restored.
4. W1/W9 need the final custom-event append/sequence boundary before
   `AppendEventCharged` and `chargeStorageTx` can be deleted. The old charged
   route returns a sequence; the existing raw append returns only an error and
   does not preserve its event-limit checks. No competing contract was added.
5. Remaining W1b legacy families include claim-time credits, paid grants, raw
   storage counters, direct uploads and old node logs. W7 must connect upload
   verification, trusted ReplicaID, physical deletion and shared edge/namespace
   rules. Existing store tests use contract fakes for those foreign stubs.
6. W1/W7 must coordinate elected maintenance, allowance/nonpayment eviction,
   record/pin policy, GC and identity/receipt pruning. W2/W7 own signing,
   retention classes, lifecycle checks, proxy egress and ring fallback.

No new human decision was made. The missing integrations and legacy families
prevent a COMPLETE claim.

## Shared files

- `internal/acceptance/store_data.go`, new `store_logs.go`: persistence dispatch
  and the SQLite log proof.
- `pkg/controller/handlers.go`, `server.go`, `tenant_routes.go`: endpoint cut.
- New `pkg/controller/store_manifest_internal_test.go`, plus
  `storage_test.go`, `node_claim_ownership_test.go`,
  `node_client_contract_test.go`: removed-route proof and obsolete assertions.
- `internal/orchestrator/hosted_retry.go`, `wingd_api_routes_test.go`: route
  policy/inventory deletion and the team quota path correction.
- `.apidiff/pkg_store.txt`, `api/openapi.yaml`, `docs/api-reference.md`,
  `docs/self-hosting.md`, `CHANGELOG.md`, their generated `pkg/docs` mirrors,
  and `design/rewrite/contracts/c1-data.md`.

Owned store changes are `storage.go`, `storage_test.go`,
`upload_quota_runtime_test.go` and this handoff. No W1 implementation file changed.

Friction: the first quota fixture used an invalid memo record name and was
corrected. A controller probe exposed the missing legacy manifest column, which
led to deleting its endpoint. The old log pruner still has a maintenance caller;
its family was left intact. API snapshot and SQL guard debt remain inherited.

STATUS: CONTINUE
