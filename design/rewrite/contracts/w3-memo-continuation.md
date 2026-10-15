# W3 memo verification continuation

<!-- design:begin -->

W3 remains incomplete. Earlier source, lease, executor, artifact and depcache
work is retained. This continuation implements candidate verification through
the declared byte/record interfaces. It does not complete owner materialization.

## Implementation

The invariant is that a usable memo hit has every declared result reference,
authoritative readable bytes, verified EOF and successful reader close.
A failed check returns an empty miss. Parent cancellation remains an error.

MemoRecords.Lookup uses the current binding and scoped key. Local candidates
require HasMany for every unique digest, then verify the output, manifest and
each member. It reuses the artifact metadata validator and required-read codec.
Output obeys the existing JSON and size rule. A declared result cannot disappear
into successful empty output. Explicit null, absent undeclared output and empty
manifests remain valid. Returned local read sets must name the current attempt
when the authority has one. No client-supplied digest grants authority.

MemoRecords.Copy sends the exact request to the record store's atomic CopyMemo
operation. It checks current authority/destination first and verifies the returned
candidate before exposing success. Failed record copies become misses. W1 still
owns transactional existence, edges, fences and lifecycle materialization.

An explicit RemoteCache permits lookup after a clean local miss. Its artifacts
are downloaded only through that interface. No remote digest enters the local
byte client, and no remote copy or publication is attempted. The remote profile
may name a different team. W7 still enforces live user authority and trusted
default-branch provenance.

The only new mechanism is the MemoRecords adapter. It is needed because memo
uses a miss policy while artifact/output reads fail execution. Byte verification
and metadata validation remain shared. No default deadline, customer limit,
environment variable, daemon or compatibility path was added.

## Contract changes needed in other areas

1. C1/W9 must connect LookupMemo's hit to one definitive MaterializeSource.
   MemoHit contains digests and a ReadSet but no winning memo RecordKey/source
   identity. ReadSet may contain several edges and declares no primary record.
   The host must not guess one or depend on the original run surviving retention.
2. C1 must reconcile CopyMemo's positive AttemptIdentity destination with
   owner-side hits before compute/start. AttemptIdentity requires a positive
   ordinal, but unexecuted materialization must consume none. The shared Copy
   adapter supports the declared positive-attempt case. It cannot fabricate an
   attempt to bridge this gap. ReconcileNode must own the atomic result/event.
3. C1/W7 must specify the memo publication representation. CommitRecordRequest
   has a required Digest and optional ManifestDigest, while MemoHit has optional
   Output and Manifest digests. No declared encoding connects these shapes for
   jobs without output, or names their typed record references. A host-selected
   envelope would silently create another area's storage contract.
4. Remote-to-local memo import needs a declared atomic reference/receipt path.
   RemoteCache is read-only and its MemoHit is not a local upload receipt.
   Verified downloads alone cannot authorize local result attachment.

The earlier full-snapshot/terminal-reference, owner-policy/approval,
cross-process-mask, selected-engine-handoff and W6 Git/environment-digest gaps
remain in the linked W3 reports. No foreign contract was changed.

## Acceptance and scope

New active cells are storage-memo-materialization-failures/hermetic-memo-records
and storage-local-remote-cache-opt-in/hermetic-memo-opt-in under TestStorage.
FuzzMemoVerification generates output/manifest/member failures and arbitrary
bytes. FuzzMemoScope mutates authority, repo/ref, job and destination fences.
Every refused copy is checked before any backend operation.

The checks carry read-side behaviors from the parked follower-output,
manifest-copy and retry-artifact tests. They close **0 additional parked rows**.
Native owner/follower execution, atomic lifecycle events, publication and the
real store/storage integrations remain open. No native target cell was unskipped.

Deleted legacy host code: **0 lines**. Required-read bodies were extracted into
shared helpers rather than copied. RunHosted, HostTriggerRun, pod assembly and
the legacy scheduler/session/layers/routes/tests still need the coordinated cut.

All production edits are in internal/nodehost: memo_records.go,
artifact_records.go and output_records.go. No production caller in another
package needed migration. Shared files are internal/acceptance/host_memo.go,
tests/acceptance/memo_test.go and skeleton_test.go, c3-protocol-sdk.md,
t1-port-inventory.md and this report.
No public docs impact is claimed because native commands still use the legacy
assembly. C3 and its acceptance checks describe the added library API.

## Verification

Every Go command uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W3/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W3/tmp.
Use go -C /home/bombuntu/.xwing/envs/1081/sparkwing from another directory.
Tests use at most -p 4.

The first acceptance compile failed because MemoRecords was absent.
The implemented memo/storage cells and the full source/lease/acceptance suites
pass. Final verification results are:

| Check | Result |
| --- | --- |
| Whole-repository go build -p 4 ./... | Pass |
| Vet for nodehost/source/lease/orchestrator and both acceptance packages | Pass |
| Full source/lease and both acceptance suites | Pass |
| Focused host suite from the depcache continuation | Pass |
| Storage acceptance and memo fuzz seeds with -race | Pass |
| FuzzMemoVerification, 15 seconds, two workers | Pass, 14,109 executions |
| FuzzMemoScope, 75,000 executions, two workers | Pass |
| sleepcheck -base HEAD and git diff --check | Pass |
| commentcheck -base main | One inherited detached HereBearer comment in internal/controllerwire/contracts.go:47. No W3 finding. |

The first timed FuzzMemoScope run reached 70,133 executions without a property
failure, then failed at its 15-second limit with context deadline exceeded.
No failing input was written. The execution-count diagnostic passed 75,000
cases. The installed Go 1.26.9 coordinator selects its parent Done before
comparing the error with its child context error. That cancellation ordering
offers a possible explanation, not a reproduced toolchain diagnosis. The
timed run remains failed evidence. No product or harness workaround was added.

To rerun the generated boundaries with the environment above:

```text
go test -p 4 ./tests/acceptance -run '^$' -fuzz '^FuzzMemoVerification$' -fuzztime=15s -parallel=2
go test -p 4 ./tests/acceptance -run '^$' -fuzz '^FuzzMemoScope$' -fuzztime=75000x -parallel=2
```

Git configuration retains the default identity and has no injected core.worktree.

The lead retains independent review, Sparkwing pipelines/gates, native
placements, HTTP/api.sock, Postgres/S3, Windows/Darwin and real-repository QA.
The broad legacy host suites retain their earlier schema/socket failures and
were not represented as passing. No Sparkwing binary, external service or git
write ran. Worker scratch is removed at shutdown.

<!-- design:end -->
