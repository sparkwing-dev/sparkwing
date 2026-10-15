# W7 storage handoff

W7 remains incomplete. This session implements filesystem log expiry, S3 log
reads and S3 lifecycle readiness. Earlier committed storage work is preserved.
No frozen interface signature or schema changed.

## Implemented

- `Filesystem.ExpireLogs` expires segment and manifest files at the age encoded
  in their stored class. It includes retired classes. Identical upload retries
  keep the original file age. CAS data, upload temporary files and unrecognized
  paths are preserved. Cancellation and filesystem errors fail loudly and
  retain the count already deleted. The controller must invoke this method in
  its leased maintenance pass before declaring filesystem retention ready.
- `S3.Get` opens exact log keys for `CommittedReader`. It refuses a response
  without a bounded nonnegative length, closes rejected bodies and maps missing
  objects to `blob.ErrNotFound`. Segment integrity and raw-size verification
  remain in the committed reader before response emission.
- `S3.LogClassReady` reads bucket lifecycle configuration for the existing batch
  signer callback. It requires an enabled prefix rule with the stored class's
  days. Conditional tag/size rules cannot establish readiness for the class.
  Bucket configuration remains operator-owned. Backend failures retain their
  cause. No bucket write or process-local readiness cache was added.
- `blob.LogClassDays` owns canonical class parsing. `logseg.ClassDays` is removed,
  and every caller is migrated. The shared lifecycle predicate moved from the
  controller into blob storage. Large day values cannot wrap into a different
  S3 expiration rule or overflow filesystem expiry arithmetic.
- The existing live-watch test now accepts the valid pending suffix when the
  fake uploader signals before the sink records completion. It still requires
  contiguous bytes at the reported offset, line 4 while watched, and no line 5
  after the watch ends. The full race run exposed the old assertion's ordering
  assumption. Production sink behavior is unchanged.

## Deleted and ported

Removed **33 lines** of parser/lifecycle code from their former locations and
**30 lines** of the old controller lifecycle test. Those implementations and
cases are ported to the storage owner. This is relocation, not deletion of the
active legacy log runtime. No compatibility alias was added.

`TestLifecycleRuleMustExpireTheClassPrefixAfterItsDays` moves from
`pkg/controller/log_retention_internal_test.go` to
`internal/blob/log_lifecycle_test.go`. Its original seven cases remain, using
final log prefixes. Added cases cover size and conjunction filters, zero days
and integer overflow. **Additional T1 port-inventory ports: 0.** The earlier
source-import ports remain committed.

Seven new test functions cover canonical classes, filesystem expiry and
preservation, exact bounded S3 reads, missing/invalid/cancelled reads, lifecycle
readiness/errors and committed reads through S3 for every tagged subject.
S3 transport and persisted record authority are fakes. Filesystem expiry uses
real files and signed uploads. No component proof counts as a native receipt.

## Acceptance and verification

**Native storage acceptance: 0 passing, 424 skipped, 0 failing target cells.**
`acceptance.New` and `Run` still panic as contract stubs. The catalog, port and
invariant checks pass. No native cell was unskipped because its placement driver
and real authority checks are absent.

Every Go command uses `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and `GOCACHE`,
`GOTMPDIR` and `TMPDIR` below `/tmp/sw-astra-W7`. Builds/tests use `-p 2` and
source tools use `-p 1`.

Passed:

- Whole-repository `go build -p 2 ./...`.
- `go vet -p 2 ./internal/blob/... ./pkg/storage/logseg ./pkg/controller`.
- `go test -p 2 ./internal/blob/... ./internal/artifacts ./pkg/storage/logseg -count=1`.
- `go test -race -p 2 ./internal/blob ./pkg/storage/logseg -count=1` after correcting
  the watch test. Its focused race run also passes with `-count=50`.
- The controller's `TestLogClassFollowsTheOperatorMapThenTheTeamsStanding`.
- `go test -p 2 -json ./tests/acceptance -run 'Test(Storage$|Catalog|Port|Invariant)' -count=1`.
- Embedded-doc privacy and changelog checks, plus `TestPkgDocsContentMatchesDocsRoot`.
- Changed-line commentcheck and sleepcheck against HEAD, including untracked Go
  files. Storage/changelog mirrors match their sources. The logseg API snapshot
  matches generated output. `git diff --check` passes.

`TestTeamDeletionReachesARetiredRetentionClass` cannot set up its legacy store:
`SQL logic error: no such table: sparkwing_meta (1)`. It fails identically with
all six changed tracked Go sources restored from HEAD using a read-only Go
build overlay. This is an existing old-store fixture failure, not a passing
controller integration. The test cannot exercise the moved parser on this tree.

The new APIs were tested red before implementation. No Sparkwing executable,
pipeline, daemon or Git write ran. Independent review and full gates remain
with the lead. Native S3, PostgreSQL, Windows and cluster runs are unverified.

## Contract changes needed in other areas

1. **W1: atomic upload lifecycle.** Define durable immutable-ticket creation,
   commit/replay and expiry release. Commit must reauthorize the exact uploader,
   publish a live blob and receipt, and settle unique-byte quota together.
   Separate receipt/quota commits and process-memory tickets are unsuitable.
2. **W1/W2/W9: working-tree submission.** Admission seals a source digest but
   returns no normal upload authority. Define how the submitter obtains upload
   authority and when execution may observe the sealed source.
3. **W1/W2: signing projection.** Expose the run's immutable retention class
   with current writer authority. The schema has `log_retention_class`, but
   final run projections provide no named accessor. BatchSigner consumes the
   resolved class and backend lifecycle callback without recomputing standing.
4. **W1/W2/storage: S3 immutable uploads and replay.** LogUploadURL and
   LogUploadBatch carry URLs without required header maps. S3 conditional PUT
   needs the signed `If-None-Match: *` header. An uncertain successful PUT replays
   as 412. Define required header transport and digest-verified replay/commit
   with current subject authority. Future signing slots also lack digest/size
   admission metadata. No unsigned overwrite or unchecked 412 success was added.

## Remaining integration and deletion

- W1 must implement records/read sets, memo-copy transactions, live blob rows,
  unique-byte quota, retention/tombstones and upload tickets.
- W2 must bind capability-aware data/record routes, tagged log signing/commits
  and proxied reads. Reauthorize after request bodies and before commit. Meter
  ReadResult.Bytes on success and failure with the log download-cap exemption.
  Bind S3 lifecycle readiness and exact-key reads. Schedule filesystem expiry
  through the maintenance lease before supplying its lifecycle-ready callback.
- W1/W2 must enforce exact persisted writers, final-job tail grace, current
  ancestry/membership and no newer claim. Eval has no job grace.
- W3/W8 must bind artifacts, memo, depcache, binary/source and tagged logs per
  attempt. Retry/generator materialization and binary provenance must run
  through those bindings. W4 must finish Cache integration and lint declaration.
- Replace and delete controller/client attempt-only signing/sealing, controller
  stream/ring authority and orchestrator legacy-log paths. Delete AttemptPrefix
  and legacy manifest identity fields after their callers are replaced.
- Delete old data handlers, cache-grant signing and service-token storage
  accounting after final routes bind. T1/lead must supply native acceptance
  drivers and execute each selected target. Follow/polling remains controller-owned.

## Assumptions and mechanisms

Filesystem expiry uses immutable file age because S3 lifecycle uses object age
and no extra per-file metadata is needed. It is an explicit method called by the
controller's leased maintenance pass. A second background loop would duplicate
maintenance ownership. Reverting the method and its tests removes this seam.

S3 opens exact log objects and reads lifecycle configuration through its existing
client. Listings cannot authorize logs. Lifecycle parsing and class parsing are
shared with existing callers because a second policy implementation would drift.
No new environment variable, limit, setting, service, cache or compatibility
mode was added. The new storage methods are integration points, not active routes.

## Shared files and friction

Minimal caller migrations outside W7:

- `pkg/controller/deletion.go` uses the canonical class parser.
- `pkg/controller/log_retention.go` uses the parser and shared lifecycle predicate.
- `pkg/controller/log_retention_internal_test.go` loses the ported lifecycle test.

Other shared files:

- `.apidiff/pkg_storage_logseg.txt`.
- `docs/storage.md` and `pkg/docs/mirror/storage.md`.
- `CHANGELOG.md` and `pkg/docs/changelog.md`.
- This handoff file.

Friction is the stubbed native acceptance driver, absent transactional bindings,
the legacy-store fixture failure reproduced above, and the live-watch test's
ordering assumption, corrected here. All writes stay inside the worktree and
permitted scratch tree. Scratch and caches are removed at handoff.

STATUS: CONTINUE
