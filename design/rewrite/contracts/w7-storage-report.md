# W7 storage handoff

W7 remains incomplete. This session adds the output-record component and fixes
remote memo lookup and verification. Earlier blob, artifact, memo, depcache,
binary, source-bundle, tagged-log, S3 signing and direct-Git work remains on this
branch. Continue from that implementation.

Implemented:

- `blob.OutputData.Put` enforces the existing 16 MiB output cap, checks the exact
  job or imported-publication binding, obtains a verified upload receipt and
  commits the output record. Empty output has an explicit blob and record.
- `OutputData.Read` resolves the authorized record and consumes bounded bytes
  under the lookup deadline. Missing records/objects, corrupt or truncated
  bytes, unverified EOF and close failures return errors without output bytes.
- Output records use the run ID as `Name`, the authorized repo/ref/pipeline/job
  scope and an empty scope key. Attempt and generation remain in `Source`.
  The host must attach the record key through its result transition.
- Memo materialization shares the bounded output reader. Remote memo reads
  verify manifest/member agreement and member sizes before granting artifact
  access. A rejected lookup grants no artifact access.
- Remote memo lookup uses `MemoRecordName` (`result`), matching the writer.
  It previously requested an empty name. Remote memo/depcache keep caller
  cancellation as an error while applying their miss/cold policy to failures.

Deleted the separate remote-cache byte verifier and the duplicate memo-output
read logic. No old subsystem was deleted. The Go diff adds **441 lines** and
removes **53**, including tests. Runtime adds **144** and removes **48**, net
**+96**. Untracked output files are included. Markdown is excluded.

The new W7-local surface is `OutputData` plus the shared memo record name.
It uses the existing `Store` and `RecordStore` contracts. A separate output
component is needed because the final output record path had no implementation.
The smaller alternative, copying output checks into each host, duplicates the
size/EOF/close policy. The component shares that reader with memo instead.
No setting, limit, environment variable, dependency, service or retry loop was
added. Reverting this session removes the additions and restores the prior reads.

Verification passed with `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `-p 4`, and
`GOCACHE`, `GOTMPDIR` and `TMPDIR` below `/tmp/sw-astra-W7`:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4 ./internal/blob/... ./internal/nodehost`.
- Full tests for `internal/blob/...`, `internal/artifacts`, `internal/depcache`
  and `pkg/storage/logseg`.
- Full race tests for `internal/blob/...`, followed by a race run covering all
  output and remote-cache tests after the imported-publication test was added.
- `internal/nodehost/TestMemoMaterializationRunsOnRefusal`.
- Acceptance catalog, invariant and port-inventory checks.
- Changed-line commentcheck and sleepcheck, including untracked files.
  Formatting and `git diff --check` pass. Shared Git configuration has no
  test identity or `core.worktree` override.

Tests came first. The remote-key test failed for the empty lookup name. After
that fix, four manifest/member mismatch cases failed. The cancellation cases
also failed before their fix. Output tests initially failed to compile because
`OutputData` was absent. All now pass. Five new test functions cover output
round trips at the exact cap, refused writes, imported publication, required-read
failures and remote memo failure policy. Persistence, authority and object HTTP
responses are fakes. New T1 inventory ports: **0**.

Storage acceptance remains **0 passing, 424 skipped, 0 failing target cells**.
`acceptance.New` and `Run` remain stubs. No native cell was unskipped or claimed
from component evidence.

Contract changes needed in other areas remain:

1. W1 must define immutable upload-ticket creation, atomic commit/replay and
   expiry release. Commit must reauthorize the uploader and publish the live
   blob/receipt with unique-byte quota settlement.
2. W1/W2/W9 must define working-tree upload authority before run creation and
   when execution may observe the sealed source digest.
3. W1/W2 must expose stored log retention class with current writer authority
   for signing. Standing must not be recomputed for an existing run.
4. W1/W2/storage must define digest-verified S3 log replay after uncertain PUTs
   or failed record commits, plus payload admission for future slots. HTTP 412
   proves no byte match. The existing create-only PUT/header work remains.

Real integration and remaining cuts:

- W1 implements RecordStore, read sets, receipts, live blobs, upload tickets,
  quota, memo-copy transactions, pins, retention and tombstones. Memo copies
  must create destination output/manifest records using the documented layouts.
- W2 binds capability-aware data/record and tagged-log routes, reauthorizes at
  commit, meters partial log reads, and wires lifecycle readiness and expiry.
- W3/W8 bind output, artifact, memo, depcache, binary, source and log components
  to fresh attempt authority. OutputData has component tests but no runtime
  binding yet. W4 completes Cache integration and declaration linting.
- Replace/delete old controller/client data and output handlers, cache grants
  and service-token accounting. Replace/delete attempt-only log signing,
  sealing, stream/ring authority and orchestrator paths, then remove
  AttemptPrefix and legacy manifest fields. Native acceptance remains open.

No new product question or cross-area type change arose. Shared runtime file
changed: `internal/nodehost/memo.go`, one line using the shared record name.
Shared contract prose changed in `design/rewrite/contracts/c1-data.md`.
Storage documentation changed in `internal/blob/README.md`. These pages have no
generated mirrors. The component tests are their behavior checks.

Friction: the shared native driver and store implementation remain stubs.
The missing upload lifecycle and log-authority projections still need contract
work by their owners. Existing interfaces were exercised with fakes. The lead
retains pipelines, native checks, independent review, committing and landing.
No Sparkwing binary, daemon, service or Git write ran. All check processes
finished before scratch cleanup. `/tmp/sw-astra-W7` is deleted.

STATUS: CONTINUE
