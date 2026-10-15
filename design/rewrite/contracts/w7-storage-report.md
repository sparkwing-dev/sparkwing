# W7 storage handoff

W7 remains incomplete. This session implements tagged log upload and committed
segment reads, and removes exported-log object discovery. Earlier committed
blob backends, artifacts, memo, depcache, binary/source clients and cache-service
deletions remain in place. No frozen interface signature or schema changed.

## Implemented

- `logseg.Data` binds one `blob.LogSubject` to `blob.LogTransport`, signed
  transfers, `Tenant.RecordSegments` and an actor. The uploader copies its
  identity. Job, trigger, plan and eval prefixes are distinct. Eval keeps its
  own generation and the immutable source generation separately.
- `SubjectUploader` checks compressed digest, stored/raw size and line coverage
  before signing. It checks batch identity, dense slots, URL uniqueness, expiry
  and retention continuity. It advances offsets only after record commit.
  Failed transfers, close failures and uncertain commits remain retryable with
  the same bytes. A changed retry cannot overwrite an uncertain segment.
- The final manifest seals the exact committed list and carries the tagged
  subject. Its totals derive from committed segments. A complete seal cannot
  hide pending bytes, drops or gaps. The first final payload survives uncertain
  writes. Changed finals fail, exact successful replays add no commit.
- `CommittedReader` calls `Tenant.ReadLogSegments` on every read. It validates
  subject, keys and contiguous ranges before fetching objects. Each segment
  must close and pass digest/raw-size verification before emission. Offset
  resume uses uncompressed bytes. `ReadResult.Bytes` includes partial writes
  before failure so the controller can meter exactly what it sent.
- `Data.Read` uses the controller-proxied transport. Failed responses close
  their bodies and preserve both request and close errors. Cancelled calls do
  not start signing, reads or commits.
- The log reader size bound and seal grace now derive from C4 limits. Their
  values are unchanged. The client checks expiry without trying to enforce
  the server's URL lifetime against its own clock.
- Controller log reads no longer discover unrecorded objects by listing
  retention prefixes. Persisted log records are their index.

## Deleted

The removed export-discovery code is counted separately from formatting and
replacement code:

| Removed surface | Lines |
| --- | ---: |
| `FindAttempts`, `Attempt`, `UnescapeNode` and their comments | 75 |
| Export/migration-only constants and comments | 5 |
| Controller object-listing fallback lines | 27 |
| Obsolete unrecorded-log listing test | 25 |
| Stale `.apidiff/pkg_storage_sparkwingcache.txt` | 10 |

Total: **142 lines**. The API snapshot is the only whole file deleted this
session. The controller's old unrecorded-log test now expects no readable bytes.
The old cache service and source transport were deleted in earlier commits.

## Tests and acceptance

**Native storage acceptance: 0 passing cells, 424 skipped cells, 0 failed cells.**
`acceptance.New` and `Run` are still stubs. No skeleton skip was removed because
these component proofs do not execute the native matrix.

The tagged uploader and committed reader have 24 test functions, including the
four untracked uploader tests present at session start. Coverage includes every
subject variant, all fences in keys, identity/retention substitution, metadata
and byte corruption, missing bytes, exact replay, uncertain writes, final seals,
response cleanup, close errors, cancellation and partial output counts. A real
`Sink` test exercises compression, spooling, commits and attributed final seals.
Concurrent exact retries have race coverage.

No additional T1 inventory port is claimed complete. Existing sink invariants
are exercised through the new uploader. The unrecorded-object test reverses an
obsolete export compatibility expectation, but its old controller fixture does
not execute against the rewritten schema. The T1 log-holder/body-stall races
still need native store and controller execution. The two source-import ports
from the previous report remain committed and are not redone here.

## Verification

Go commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-astra-W7/go-cache`, `GOTMPDIR=/tmp/sw-astra-W7/go-tmp` and
`TMPDIR=/tmp/sw-astra-W7/tmp`. Every Go build/test uses `-p 4` or less.

Passed:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4 ./pkg/storage/logseg ./pkg/controller`.
- Full `go test -p 4 ./pkg/storage/logseg ./internal/blob/... ./internal/artifacts -count=1`.
- Full `go test -race -p 4 ./pkg/storage/logseg -count=1`.
- Test compilation for controller, controller/client, orchestrator and launcher
  with `-run '^$'`.
- Acceptance catalog, port inventory and invariant checks with
  `go test -p 4 ./tests/acceptance -run 'Test(Catalog|Port|Invariant)' -count=1`.
- Storage skeleton execution with JSON evidence, counted as the 424 skips above.
- Changed-line commentcheck/sleepcheck, source/embedded docs sync and
  `git diff --check`.
- Logseg API snapshot generated by `cmd/apidiff` and compared with a second,
  independently generated snapshot.

Independent snapshot review reproduced two defects: a valid signing batch was
refused when the controller clock was one second ahead, and an uncertain
segment commit could be followed by a false complete seal. Both now have
repository regressions that failed before their fixes and pass afterward.
Full snapshot tests, review probes and focused races pass.

The reviewer attempted legacy controller tests. Their fixtures fail before
behavior with `no such table: sparkwing_meta`. They establish no runtime result.
An initial C4 constant substitution also exposed an int/int64 compile mismatch;
the length comparison was corrected. Review also caught an unnecessary public
constant type change in the sink cleanup. That cleanup was reverted. Final
verification was rerun.

## Contract changes needed in other areas

1. **W1: atomic upload lifecycle.** Verified receipts and quota settlement do
   not expose the durable immutable-ticket transaction needed to reauthorize
   the uploader, publish a live blob and exact receipt, and settle unique-byte
   quota together. Replay, expiry release and interrupted commits need that seam.
   No process-memory tickets or separately committed receipts were introduced.
2. **W1/W2/W9: working-tree submission.** `CreateRunRequest` seals the source
   digest, but normal admission returns no upload authority. Upload requires a
   live claim or run-bound publisher. Define the submission/upload handoff and
   when execution can observe the sealed source. The CLI and old hosts still
   lack source bindings, so working-tree triggers fail before submission.
3. **W1/W2: signing policy projection.** The schema persists
   `runs.log_retention_class` and the run owns the class, but final `RunRow` and
   `RunSnapshot` expose no class and no final named lookup exposes it. The log
   signer needs that immutable value alongside current writer authorization.
   Expose it through the final tenant contract rather than recomputing standing
   or using raw controller SQL. This session leaves that contract unchanged.

## Remaining real integrations and W7 work

- W1 must implement records/read sets, memo-copy transactions, live blob
  existence, unique-byte usage, retention/tombstones and upload lifecycle.
- W2/W7 must bind capability-aware data/record HTTP handlers and log signing,
  commits and proxied reads. Authorize before signing and reauthorize at commit.
  The signer owns the fifteen-minute lifetime and retention lifecycle checks.
  Meter `ReadResult.Bytes` on success and failure, with the log quota exemption.
- W1/C2 must enforce exact persisted writers, final-job tail grace, current
  credential ancestry/membership and no newer claim. Eval gets no job grace.
- W3/W8 must bind artifacts, memo, depcache, binary and source clients per
  attempt, and pass tagged log data into sinks. Finish retry/generator/memo
  materialization and ensure machine binary reuse cannot bypass provenance.
- The active controller/client log signer/sealer, controller stream/ring path,
  orchestrator log path, `AttemptPrefix` and attempt-only manifest fields still
  need migration and deletion. The tagged components have no adapter to them.
  Follow/polling and ring fallback stay controller-owned.
- Old controller data handlers, cache-grant signing and service-token storage
  accounting remain to be deleted when their final bindings land.
- W4 owns the remaining `.Cache` integration and lint declaration. T1/lead own
  native drivers and every storage acceptance cell.

## Mechanisms and assumptions

`Data`, `SegmentRecorder` and `SegmentLookup` are local bindings of the frozen
interfaces. They allow testing the real storage implementation while the other
owners' methods are stubs. The uploader's pending/final/sealed state prevents
changed retries after uncertain immutable writes. The reader reuses `Objects`
and keeps polling outside storage. Independent review found these mechanisms
proportionate. No environment variable, service, compatibility adapter or new
customer limit was added. Removing the two implementation files and their tests
reverts this component seam without changing frozen contracts.

## Shared files and friction

Outside `pkg/storage/logseg`:

- `pkg/controller/log_reads.go` and `log_segments_internal_test.go`.
- `.apidiff/pkg_storage_logseg.txt` and deleted
  `.apidiff/pkg_storage_sparkwingcache.txt`.
- `docs/storage.md`, `CHANGELOG.md` and their generated embedded copies.
- This handoff file.

The API generator also reports pre-existing drift for controller/client,
projectconfig, match, store, storage and storage/storeurl, and the public SDK.
Those other owners' snapshots were left unchanged. No all-package API snapshot
pass is claimed.

Native controller fixtures are blocked by the unported store schema. HTTP and
service tests need the lead's socket-enabled environment. Windows cleanup is
unexecuted. Cleanup policy refused the shell deletion command. Exact-path
checked Python cleanup succeeded. No Sparkwing binary, pipeline, daemon or Git
write ran.

Git still resolves Korey Gambill and has no `core.worktree` override. Writes
stayed in the authorized worktree and `/tmp/sw-astra-W7`. All scratch, caches
and review snapshots are removed at handoff.

STATUS: CONTINUE
