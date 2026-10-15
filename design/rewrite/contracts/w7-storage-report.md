# W7 storage handoff

W7 remains incomplete. This session removes the old local binary-cache layout
and closes artifact/depcache download errors before publication. Earlier
committed byte, record-client, source, memo and tagged-log work is preserved.
No frozen interface signature, schema, dependency or environment variable changed.

## Implemented and deleted

- Local binary-cache status and pruning operate only on the current entry
  layout. Removed old-directory discovery, quarantine, grace-period retirement,
  Windows retirement probes and compatibility accounting. Removed
  `CacheStatus.LegacyEntries`, `CacheStatus.LegacyBytes` and their CLI fields.
  The CLI caller and pruning ceiling calculation use current entries only.
- Kept current-cache capacity accounting on every supported build target.
  Renamed the three `legacy_capacity_*` files to `capacity_*`, and retained their
  two applicable capacity tests in `capacity_unix_test.go`. These measure actual
  capacity gained and prevent crediting unrelated filesystem changes.
- Removed five obsolete legacy-only test functions and their platform helpers.
  Added `TestPruneOnlyCurrentCacheLayout` and updated the status test. Real files
  prove that pruning reclaims the current entry and leaves outside layouts alone.
- Artifact staging checks both manifest and member download close errors before
  publishing that object's files. Failed member downloads remove staging files
  and preserve the member's existing destination. Previously the deferred close
  discarded the error after replacement.
- Dependency-cache restore closes its verified download before extraction. A
  close error returns to the caller for a cold start and preserves the existing
  destination. Tests inject failures through the byte-store/transfer interfaces.

Across all changed Go files, including retained helpers under their new names,
this session removes a net **393 lines: 205 implementation and 188 test lines**.
This is a net count after the new regression tests and close checks. It does not
claim that the remaining legacy controller log/data runtime is deleted.

## Verification

Every Go command uses `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and `GOCACHE`,
`GOTMPDIR` and `TMPDIR` below `/tmp/sw-astra-W7`. Commands use `-p 2`, except
source-policy tools, which use `-p 1`.

Passed:

- Final whole-repository `go build -p 2 ./...`.
- `go vet -p 2 ./internal/bincache ./internal/blob/... ./internal/artifacts
  ./internal/depcache ./pkg/storage/logseg ./cmd/sparkwing`.
- Full tests for `internal/blob/...`, `internal/artifacts`, `internal/depcache`
  and `pkg/storage/logseg`, with `-count=1`.
- The same storage packages under `go test -race -p 2`, with `-count=1`.
- Binary-cache entry, queue, prune and capacity tests, selected from their test
  function names, both ordinary and race runs. The initial focused command was
  `go test -p 2 ./internal/bincache -run
  'Test(Prune|Status|Removal|ManagedRemoval)' -count=1`.
- `GOOS=windows GOARCH=amd64 go test -p 2 -c` for `internal/bincache`. This is
  compilation evidence only.
- `go test -p 2 ./cmd/sparkwing -run '^TestCache' -count=1`.
- Acceptance catalog/port/invariant checks and `TestStorage` skeleton enumeration.
- Embedded documentation/changelog drift and privacy checks. Updated storage
  documentation, migration steps, changelog and their embedded mirrors.
- Changed-line commentcheck and sleepcheck against HEAD, including untracked
  files. `git diff --check` passes. Git config has no injected test identity or
  worktree override. No test helper processes remain.

The new prune and close-failure tests ran red before their implementations.
The full binary-cache suite stops in
`TestRequestDirectCredentialReadsEachKind/https`: `httptest` cannot open its
listener because the sandbox denies sockets. The targeted cache suite exercises
all changed cache behavior without that listener. No production test was skipped
or altered to bypass the sandbox.

**Native storage acceptance: 0 passing, 424 skipped, 0 failing target cells.**
`acceptance.New` and `Run` remain stubbed. All catalog targets are native, cluster
or hosted. Component tests with fakes cannot satisfy those cells, so none was
unskipped. **Additional T1 inventory ports this session: 0.** Earlier source
ports remain committed. Two existing capacity tests remain under their new file.

No Sparkwing executable, pipeline, daemon, Git write or release ran. The lead
retains independent review, full gates and native execution. Scratch is removed
at handoff.

## Contract changes needed in other areas

The previously reported contract gaps remain:

1. **W1: atomic upload lifecycle.** Define durable immutable-ticket creation,
   commit/replay and expiry release. Commit must reauthorize the exact uploader,
   publish the live blob/receipt and settle unique-byte quota together.
2. **W1/W2/W9: working-tree submission.** Define the submitter's normal upload
   authority and when execution can observe the sealed source digest.
3. **W1/W2: log signing projection.** Expose the immutable stored retention class
   with current writer authority. BatchSigner consumes that projection and
   backend lifecycle readiness without recomputing standing.
4. **W1/W2/storage: S3 log PUT headers and replay.** The URL-only log contract
   cannot transport signed conditional headers. Define header transport and
   digest-verified replay/commit after an uncertain upload. Future slots also
   lack admitted size/digest metadata. No unchecked 412 success was added.

## Real integration and remaining cuts

- W1 must implement RecordStore, read sets, memo-copy transactions, live blobs,
  upload tickets, unique-byte quota, retention and tombstones. Those APIs remain
  stubs. The clients are tested with fakes against the existing interfaces.
- W2 must bind capability-aware data/record routes and tagged log signing,
  commits and proxied reads. Reauthorize after bodies and at commit. Meter
  committed-read bytes on both success and failure with the log-cap exemption.
  Wire S3 lifecycle/read methods and leased filesystem log expiry.
- W1/W2 must enforce exact persisted writers, final-job grace, live ancestry and
  membership, and no newer claim. Eval keeps its independent lease rules.
- W3/W8 must supply live per-attempt bindings for artifacts, memo, depcache,
  binary/source and tagged logs, including retries and generator reconstruction.
  W4 must finish Cache integration and lint declarations.
- Replace/delete the old controller/client attempt-only signer, sealing and
  stream/ring authority, plus orchestrator legacy log paths. Then remove
  AttemptPrefix and legacy manifest identity fields.
- Replace/delete old data handlers, cache grants and service-token storage
  accounting after final routes bind. Native acceptance drivers and selected
  target execution remain open. Follow/polling remains controller-owned.

No new open question or cross-area contract change was introduced this session.

## Shared files and friction

The only runtime caller outside storage changed this session is
`cmd/sparkwing/cache.go`. It drops removed status fields and uses current totals.
Other shared changes are `docs/storage.md`, `docs/migrations/_unreleased.md`,
`CHANGELOG.md`, their three embedded mirrors and this report.

The cut assumes that pre-launch stores and caches need no compatibility path,
as authorized by the swarm brief. Current-layout lease and capacity safeguards
remain. Reverting the cache cut restores old-layout handling. No new mechanism,
limit, service, cache, retry loop, setting or compatibility surface was added.

Friction: native acceptance and store bindings remain stubbed, and the sandbox
blocks listener-based tests. The existing legacy-store fixture failure described
in the prior report was not rerun because this session changes no controller/store
code. Earlier S3, retention, authorization and host integration gaps remain open.

STATUS: CONTINUE
