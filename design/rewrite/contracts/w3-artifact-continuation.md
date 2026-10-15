# W3 required-artifact continuation

<!-- design:begin -->

W3 remains incomplete. This continuation implements required-artifact reads
against the amended byte and record interfaces. The earlier
[amendment audit](w3-amendment-handoff.md) and
[captured-environment report](w3-snapshot-continuation.md) retain the other W3
work and contract gaps.

## Implementation

`ArtifactRecords.Stage` accepts a producer's persisted manifest record key and
the consumer's live read authority. It looks up the authorized member rows,
checks their manifest/team identity and workspace paths, then verifies the
manifest bytes. Member rows from the record transaction supply membership.
The downloaded manifest body cannot add read edges or choose more files.

Each member download uses the same consumer authority. Reads go through EOF
verification and successful Close. A declared size mismatch fails. Transfers
stop after the declared size plus one byte when bytes exceed that declaration.
This follows the record's size and adds no independent artifact-size limit.
An embedded WriterTo cannot bypass Read's verification path.

Files stage through `os.Root` into private sibling temporary files. Rename
replaces the destination only after verification and close. Failed reads retain
the old destination and remove their partial file. Escaping parent symlinks
fail. Replacing a final symlink preserves its outside target. Only permission
bits are restored. Missing records, missing bytes, corruption and failed reads
return errors rather than a successful empty stage. An authorized empty manifest
still verifies its bytes and returns zero files.

The executor acceptance fixture uses its declared producer/consumer plan and
the committed first attempt's StorageBinding. Staging runs once around three
application attempts. A failed stage records NodeFailed without starting an SDK
process. Every started fake SDK joins cleanup. The dependency customization in
the existing fixture keeps the SDK and store setup shared.

## Scope and remaining work

This implements the required-read portion of A1-36 and adds hermetic evidence
for A1-51/A1-53. All other assigned amendment rows retain their previous audit
dispositions. The new ArtifactRecords API is local to W3's C3 contract. No other
area's contract shape changed, and no new external contract request was added.

Native assembly still needs a persisted producer record reference from W1/W9,
the live read-edge lookup from W1/W2/W7 and the authenticated byte client from
W7/W9. These are tested through the declared interfaces with fakes. The helper
does not infer record references from raw digests or revive ArtifactStore.

Artifact capture/publication, memo/depcache layers and read-only remote-cache
assembly remain. Owner policy transitions and approval reads, complete remote
execution snapshots, cross-process dynamic-mask propagation and matched-engine
host handoff retain the earlier reported contract gaps. Native run/trigger/pod
assembly and the legacy runtime deletion remain W3 work. The W6 Git-kind and
environment-digest failures recorded in the snapshot report have not been
reclassified as passing integration.

No production code outside internal/nodehost changed. Shared files changed in
this continuation are internal/acceptance/host_artifact.go,
internal/acceptance/host_executor.go, tests/acceptance/skeleton_test.go,
c3-protocol-sdk.md, t1-port-inventory.md and this report. No production caller
needed migration. No old implementation or test was deleted: **0 legacy lines**.
The new checks carry existing staging behaviors, but no additional parked
memo/materialization test port is claimed complete.

The five merge-conflicted paths retain resolved contents, including deletion of
internal/orchestrator/claim_exec.go. No conflict marker remains. The index still
lists them as unmerged because the worker brief prohibits git writes. The lead
must stage the resolved files and finish the merge. No commit, Sparkwing binary,
pipeline or external service ran in this continuation.

No docs impact on the public CLI: native assembly has not connected this helper
to the public command path. The C3 source contract and acceptance inventory
describe the new host behavior.

## Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false and GOMAXPROCS=4. GOCACHE,
GOTMPDIR and TMPDIR stay under /tmp/sw-astra-W3/. Tests use at most -p 4.

| Check | Result |
| --- | --- |
| Whole-repository `go build -p 4 ./...` | Pass |
| Vet for nodehost, source, lease, orchestrator and both acceptance packages | Pass |
| Full source, lease and both acceptance package suites | Pass |
| Focused host suite from the amendment handoff, plus captured-environment/native SDK tests | Pass |
| Both new artifact acceptance cells, including `-race -count=1` | Pass |
| `git diff --check` and conflict-marker scan | Pass |
| Commentcheck against HEAD | One inherited detached HereBearer comment in internal/controllerwire/contracts.go:47. No W3 finding. |
| Sleepcheck against HEAD | Two inherited wall-clock assertions in internal/limits/deadline_test.go:58 and :66. No W3 finding. |

The two new active cells are:

```text
TestStorage/storage-cas-integrity/hermetic-required-artifacts
TestStorage/storage-cas-integrity/hermetic-executor-artifacts
```

Rerun them with the environment above:

```text
go -C /home/bombuntu/.xwing/envs/1081/sparkwing test -p 4 -race ./tests/acceptance -run '^TestStorage$/^storage-cas-integrity$/^hermetic-(required|executor)-artifacts$' -count=1 -timeout 5m
```

Native placement/backend targets remain skipped. The lead retains native
Windows/Darwin, real HTTP/api.sock, Postgres, S3 and real-repository QA, plus
Sparkwing pipelines and gates. No public behavior or release is represented as
complete by these hermetic checks. Worker scratch is removed at shutdown.

<!-- design:end -->
