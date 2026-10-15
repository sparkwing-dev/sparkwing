# W7 storage handoff

W7 remains incomplete. This session replaces the binary data client with a
consumer of the frozen blob interfaces and removes unused cache-grant
process-environment transport. Earlier byte backends, artifact capture/staging,
memo materialization, dependency-cache backend and cache-service deletions remain
in place. No other area's frozen contract was changed.

## Implemented in this session

- `bincache.BinaryData` binds `blob.Store`, `blob.RecordStore`, authority,
  uploader identity, team build-trust policy and an explicit read budget.
  It is a local W7 binding, not a new wire protocol or credential.
- Binary downloads look up the exact team/repo/ref/build record and check
  `CheckBinaryExecution` before fetching its committed digest. Local provenance
  requires `trust_local_builds`. Wrong team, repository, ref or build key fails
  before reading bytes. EOF verification and stream close precede executable
  publication. Failure preserves an existing destination and removes staging.
- Binary uploads obtain a verified receipt for their owner/job identity before
  committing the record. `ResolveWriteKey` derives the namespace. The record
  store assigns provenance, and a mismatched commit response fails integrity.
- Download deadlines from lookup, transfer and stream consumption all retain
  `context.DeadlineExceeded` and classify as `blob.ErrBudget`. Cancellation stays
  cancellation. An expired budget performs no lookup.
- Migrated the binary consumers in the legacy trigger and node hosts to the
  new type. Their production call sites pass no remote binding and compile into
  the machine's binary cache. **Shared binary transfers are not integrated.**
  The source docs and changelog state this behavior.
- Removed cache-grant environment injection from launched/dispatched nodes,
  the renewal goroutine, and `CacheBearerFromEnv`/`CacheGrantEnv`. Ordinary Git
  source paths no longer request a cache grant. Working-tree bundle fetches still
  request one in memory through the remaining old source transport.

## Deleted

Two complete files, **216 lines**, are deleted:

- `internal/bincache/direct_data.go`: 47 lines.
- `internal/bincache/direct_data_test.go`: 169 lines.

Retained files also lose the duplicate binary installer and `ErrDigest`, grant
renewal/environment code, its obsolete bearer test and two cache-grant binary
fixtures. Binary integrity, cancellation and publication-cleanup assertions were
moved to `binary_records_test.go`. The signed-transfer test in `internal/blob`
still verifies that object uploads receive no controller bearer.

## Acceptance and test ports

**Native storage acceptance: 0 activated; 424 target cells remain skipped.**
`acceptance.New`/`Run` remain stubs. This session does not count fakes as native
SQLite/Postgres, filesystem/S3, Windows or placement evidence.

New component coverage exercises the binary parts of
`storage-binary-provenance`, `storage-cas-integrity` and
`storage-owner-receipts-and-stream-verification`. The byte implementation is the
real `blob.NewSignedStore`; the signer/transfer and record transaction are fakes.
It covers Cloud/local provenance, team opt-in, exact record scope, owner-unit
uploads, corrupt/short/long bytes, unverified EOF, failed close, missing records,
failed upload, cancellation, budgets and failed executable publication.

The T1 port `TestDataDownloadClaimUsesCloudProvenance` is **not fully closed**.
Its native execution/source cases and controller authority checks remain open.
No additional full T1 port is claimed complete. The legacy binary-client tests
listed above are replaced by component tests on the new contract.

## Verification

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, caches/temp files
under `/tmp/sw-astra-W7`, and `-p 2`.

Passed:

- Whole-repository `go build -p 2 ./...`, including after the final correction.
- Vet on `internal/bincache`, `internal/authwire`, `internal/orchestrator` and
  `internal/cluster`. Bincache vet was repeated after the correction.
- Full `internal/blob/...`, `internal/artifacts` and `internal/authwire` tests.
- `internal/bincache -run '^TestBinary'` and the trigger's local-build check.
- Acceptance catalog/documentation, invariant coverage and port-inventory checks.
- Embedded documentation sync check and `git diff --check`.

An initial broader selection reached the existing
`TestRequestCacheGrantAuthenticatesAsTheRunner` and failed because `httptest`
cannot open a socket in this sandbox. Full bincache, controller and host suites
are not claimed. The isolated new binary tests pass without sockets.

A fresh Astra reviewer inspected an immutable snapshot. It found inconsistent
classification of in-flight deadline errors. The new lookup/download/stream
regressions failed against that snapshot through a test overlay, and all three
pass after the fix. No blocking provenance or integrity finding remained.

## Contract changes needed in other areas

**W1's upload lifecycle transaction seam remains undeclared.** `RecordStore`
operates on already verified receipts. `ReserveQuota`/`SettleQuota` alone do not
provide a transaction that binds an immutable upload ticket, reauthorizes its
uploader, publishes the live blob and exact receipt, and settles unique-byte
quota together. W1 must declare that lifecycle, including ticket lookup/replay
and expiry release. W7 cannot replace it with process-memory tickets or separate
receipt/quota writes without violating the frozen contract. No W1 API was added.

The other areas' existing stubbed interfaces were kept intact. No new product
question or approval is pending.

## Real integration and remaining W7 work

- **W1:** implement records/read sets, memo-copy transactions, authoritative blob
  existence, unique-byte accounting, retention/tombstones and the upload
  lifecycle above. Team deletion still needs authoritative CAS/log cleanup.
- **W2/W7:** implement capability byte/record HTTP transport and final controller
  data handlers against that lifecycle. Authorize and reserve before signing,
  reauthorize before commit, and bind the existing filesystem/S3 object backend.
  C2's authorization and error-codec functions remain stubs in this worktree.
- **W7:** migrate working-tree source publication/fetch and remove `directdata`,
  source grants, old controller storage handlers and service-token accounting.
  Log/publication integration remains open. The old source and controller paths
  are not compatibility implementations of the new contracts.
- **W3/W8:** supply live per-attempt artifact/memo/depcache bindings and binary
  storage bindings. Before enabling remote binary reuse, ensure a machine cache
  hit cannot bypass changed team/provenance policy or cross team boundaries.
  Existing machine cache hits precede remote record checks. Production calls in
  this session remain unbound and build locally. Complete memoized lifecycle,
  generator replay and retry materialization under the store contract.
- **W4:** finish `.Cache` and migrate the repository lint declaration.
- **T1/lead:** implement the native driver and execute all native storage cells.
  No native controller, S3, Postgres, Kubernetes or Windows run was attempted.

## Assumptions and mechanisms

The new `BinaryData` value groups existing contracts because the old
`directdata.Client` cannot prove record scope or provenance. No environment
variable, flag, service, policy limit, compatibility shim or authority store was
added. Removing the binding and its new consumers would undo this local API
change. The reviewer found no unnecessary new abstraction.

## Shared files touched

Outside W7's binary/blob implementation:

- `internal/authwire/cachegrant.go`, `cachegrant_test.go`.
- `internal/cluster/direct_source_credential_test.go`,
  `trigger_binary_auth_test.go`, `trigger_loop.go`, `trigger_typescript_test.go`.
- `internal/orchestrator/cache_grant.go`, `claim_exec.go`, `claim_node.go`,
  `pod_node_host_test.go`, `run_node_claim_test.go`, `run_node_remote.go`,
  `run_node_verb.go`.
- `CHANGELOG.md`, `docs/storage.md`, and their generated embedded copies
  `pkg/docs/changelog.md`, `pkg/docs/mirror/storage.md`.

## Friction and cleanup

The sandbox blocks sockets, so the lead retains pipelines and native gates.
Standards discovery rejected the review snapshot because it has no `.git`;
read-only discovery against the real worktree succeeded. Initial bootstrap ran
`sparkwing info --for-agent` before the no-binary swarm instruction was loaded.
No candidate, daemon or pipeline was run, and no git write was made. Git still
uses Korey Gambill and shared `core.worktree` is unset. The scratch directory
`/tmp/sw-astra-W7` is removed at handoff.

STATUS: CONTINUE
