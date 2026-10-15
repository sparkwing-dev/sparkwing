# W7 storage continuation

<!-- design:begin -->

## Implemented

Starting HEAD is `062f60e4c`. The checkout was clean. The three merge conflicts
were already resolved. W3's hosted_run, trigger_host and pod_node_host deletions
remain intact. No Git write was made.

The existing filesystem-sink, s3-sink and filesystem round-trip properties now
use `Tenant.RecordSegments` and `Tenant.ReadLogSegments` instead of the in-memory
segment ledger. The sink, compression, batch signer, signed transfers and
committed reader remain real. Each property runs 128 generated cases covering
job, trigger, plan and eval subjects. Eval's lease generation remains separate
from its source generation. Databases close between generated cases.

The invariant is that persisted log bytes can be read only under current
authority for the exact tagged subject and generation. The properties reopen
SQLite before offset reads, refuse foreign subjects and generations, and refuse
a revoked reader without emitting bytes. Concurrent duplicate deliveries retain
one copy and per-writer ordering. Live offsets match reopened bytes. Lost PUT
responses require physical verification before the real segment transaction.
Corrupt committed objects fail before output. Both backends retain spool cleanup
and exact final-manifest checks.

Claim issuance and authenticated signing remain contract fixtures. Filesystem
objects and signed transfers are real. S3 uses the real signer and SDK against
the existing fake object service. No new acceptance cell, registry, scenario
catalog, production wrapper or contract was added. The shared job-claim fixture
was extracted because both depcache and owner-log fixtures need its persisted
authority envelope. The original depcache entrypoint retains its behavior.

## Deleted and production lines

Removed the two residual raw-storage errors `storage.ErrNotFound` and
`storage.ErrPreconditionFailed`: **7 production lines deleted**, zero added.
Object-read test callers now use the real backend's `blob.ErrNotFound`.
The unused in-memory acceptance segment ledger and its synthetic subject
constructor are deleted.

W7's net production Go delta against main is **-6,875 lines** under the lead's
area prefixes and exclusions for tests, testdata, acceptance and contract files.
That is **245 lines within** the -6,630 budget. The diff contains 4,914 added and
11,789 deleted production lines. This session's production delta is **-7**.

The cache command/service, gitcache, package proxy, cache chart, raw S3 output
and log backends, run-directory log fallback, direct source-fetch clients and
credential transport remain deleted. Historical signed release assets remain.
No legacy file was restored. The deletion manifests retain the prior cuts.

## Verification

Go commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-gocache`, and scratch beneath `/tmp/sw-astra-W7`.
Tests use `-p 4`. Pipelines, independent review and landing remain with the lead.

- Whole-repository `go build ./...`: pass after the production deletion.
- Tests for blob/..., artifacts, depcache and pkg/storage/...: pass.
- Existing `TestStorage`: pass in 135.458 seconds, 70 pass events, 424 skips,
  zero failures. Native skipped cells remain unclaimed.
- Final changed log-cell check after prompt database cleanup: pass. The three
  existing cells cover 384 generated cases in total.
- Controller `TestCommittedDashboard*` and `TestLogSubmissionRoutes*`: pass.
- Vet on storage, controller and tests/acceptance: pass.
- `internal/doccheck`: pass.
- `commentcheck -base main .`: clean across 2,185 files.
- `git diff --check`: pass.

A broader controller selection also ran `TestAnUnwiredFreeTierLogsOnceAtStartup`
and `TestTeamLogReadsAreNotRefusedPastTheDownloadCap`. Both fail in unchanged
fixtures with `tokens: insert: SQL logic error: no such table: tokens (1)`.
These fixtures belong to W2's credential migration. They were left unchanged.
The narrower tests covering the migrated caller pass. No archived test port was
added. HTTP/api.sock, PostgreSQL, native Windows/Darwin, placement, deployment,
release and real-repository proofs remain unclaimed.

## Remaining handoffs and contract changes needed

A1-24/29/30/31/32/33/34/35 retain W7's real tenant upload/record integrations and
physical backends. A1-36/37 retain the host and SDK contract consumers. A1-43's
historical assets remain. Materialization remains closed by W1b's real
controller/host byte-copy proof. A1-50 composition and A1-53 native evidence
remain assigned to their owners. Lead-owned amendments and handoffs were read
and left unchanged.

The prior session's reproduced foreign seams remain present in this checkout:

- W1/W2 account-scoped remote-cache authority loses `data.read` when
  `readAuthorityTx` intersects an unbound account grant with `ScopesForRole`.
  W2 requires that repository-bound account reader. W7 does not substitute a
  machine or widen membership policy.
- W1 A1-29's default-ref selection still constructs a record namespace before
  selecting the verified server default. RemoteCache leaves the local ref out
  as contracted. W7 does not guess the server's default branch.
- W1/W2 still need a tenant-authorized selected-output digest projection in the
  run snapshot before W1 can remove OutputRef, node_outputs and data_objects.
  The remaining output metadata and receipt caller are outside W7's byte paths.
- W2 must supply authenticated `blob.LogTransport` and `SegmentRecorder` host
  clients, including uncertain-upload verification through `UploadVerifier`.
  W7 uses those interfaces. It does not copy authentication or authorization.
- W3/W8/W9 must supply fresh data/log bindings per attempt and owner generation,
  remote-cache opt-in, and source-bundle intake in production host construction.
- W1 must expose an elected, fenced expired-ticket selector before physical
  staging cleanup can consume it. W7's deletion backends are ready.

No foreign contract was changed. W7's byte implementation and legacy cuts are
in place, but authenticated remote-reader and production host bindings remain
open. The area is therefore still CONTINUE.

## Shared files, assumptions and friction

Production edits are confined to owned `pkg/storage/storage.go`.
The one-line foreign caller migration is `pkg/controller/log_reads_test.go`.
Owned test migration is `pkg/storage/logseg/memory_test.go`.
Acceptance helpers changed in `tests/acceptance/blob_depcache_test.go`,
`storage_logs_test.go`, `storage_log_sink_test.go` and the new test-only
`storage_log_persistence_test.go`. The existing registries are unchanged.
Source documentation changed in `internal/blob/README.md` and
`pkg/storage/logseg/README.md`.

No new interface, retry mechanism, environment variable, credential policy or
production setting was added. Fixture graph intake remains seeded because
this check joins W7's sink to W1b's real persistence and W2's signing contract.
Replacing that fixture with real claim issuance is a separate native proof.

Friction: the legacy controller tests still refer to a removed tokens table.
The prior fake log ledger also hid the distinction between persisted subject
selection and synthetic read results. The existing cells now exercise the real
selection and authority checks.

Scratch `/tmp/sw-astra-W7` is removed at exit. Shared `/tmp/sw-gocache` is retained.

<!-- design:end -->

STATUS: CONTINUE
