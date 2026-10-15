# W7 storage continuation

<!-- design:begin -->

## Implemented

Starting HEAD is `f14ded68e`. The checkout was clean and already merged.
There were no remaining conflicts. W3's hosted_run, trigger_host and
pod_node_host deletions remain intact. No Git write was made.

The existing filesystem and S3 archive proofs now use W2's real
`DataController.BindData` for byte signing and every record operation.
This replaces the 71-line handwritten upload/commit/download signer.
Persisted authority resolution, current data-access checks, reservations,
physical verification, receipt settlement, download charging, manifest reads,
memo copies and dependency-cache operations all run through real store adapters.
The proofs rebind after reopening SQLite. Claim issuance is seeded and S3's
object service is injected. They do not establish native transport or placement.

Real integration reproduced `invalid blob digest` on every commit because the
controller authorized an empty digest before reading its ticket. It also exposed
the omitted accounting replica and the physical staging dependency on replay.
Three small shared controller edits close those gaps:

- Authorize the digest from the tenant-authorized persisted ticket.
- Pass the server's existing lease-holder identity as the accounting replica.
- Reauthorize committed tickets and replay their store settlement without
  republishing deleted staging bytes.

The invariant is that only the current ticket owner can settle its exact verified
bytes, and replay returns the same receipt. Both existing generated archive
proofs now check forged uploader refusal, missing-staging refusal, staging cleanup,
identical receipt replay before and after reopening, and revocation refusing both
receipt replay and record reads. Each backend exercises 128 generated payloads.
No acceptance cell, scenario catalog or registry was added or changed.

## Deleted and line delta

Seven `internal/acceptance/blob_*.go` files, totaling **1,229 production harness
lines**, moved into the existing `tests/acceptance` package as private test-only
helpers. Their existing storage cells retain the same calls and coverage.
The handcrafted signer is deleted. The new test helpers total 1,217 lines,
including the real binding and added settlement assertions.

W7's budget measurement remains **-6,866 production Go lines against main**:
15,639 baseline lines and 8,773 current lines. This is **236 lines within**
the -6,630 budget. The lead's metric excludes acceptance helpers.
The shared controller change is **+1 net production line**. Counting the moved
harness files, this session's non-test Go delta is **-1,228 lines**.

The legacy cache service/command, gitcache, package proxy, cache chart, raw S3
output backends, raw log backends and run-directory fallback remain deleted.
The preceding source-fetch and credential-transport cut remains intact.
Historical signed release assets remain intact. No old code was restored.
The existing w7-storage-pruned-files.txt records the earlier deletion manifest.

## Verification

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-gocache`, and `GOTMPDIR`/`TMPDIR` under `/tmp/sw-astra-W7`.
Tests use `-p 4` or less.

| Check | Result |
| --- | --- |
| Whole-repository `go build ./...` | Pass |
| Existing `TestStorage` acceptance suite | Pass, 104.772 seconds, 70 pass events, 424 skips, zero failures |
| Real-binding filesystem and S3 archive proofs | Pass, 128 generated cases per backend |
| `tests/acceptance` `TestData*` | Pass |
| Controller `TestData*` and `TestLogSubmission*` | Pass |
| `TestMaterializedResultCopiesBytesThroughControllerAndHost` | Pass |
| blob/..., artifacts, depcache, pkg/storage/... and doccheck tests | Pass |
| Vet on storage, controller, acceptance helpers and storage acceptance tests | Pass |
| `commentcheck -base main .` | Clean across 2,184 files |
| `git diff --check` | Pass |
| Repository Git configuration | Expected global identity, no core.worktree override |

No archived test port was added. Existing upload, manifest, log, collection,
remote-cache and archive proofs were retained in test-only files. The new
assertions extend the existing archive cells. Native Windows/Darwin, PostgreSQL,
placement, deployment, release and real-repository proofs remain unclaimed.
Pipelines and independent review remain with the lead.

## Contracts and real integration left open

A1-29, A1-30, A1-31 and A1-33 remain implemented. A1-24, A1-32, A1-34 and
A1-35 consumers use the real tenant store. A1-36 and A1-37 consumers use the
host and SDK contracts. A1-43's historical assets remain intact. A1-50 production
composition and A1-53 native evidence remain with their assigned owners.
Lead-owned handoffs and amendments were read and left unchanged.
No foreign contract or public API changed.

**W1/W2 output digest projection still needs a contract addition.** NodeRow keeps
selected OutputRecord/ManifestRecord keys, including materialized results without
an attempt. RunSnapshot still has no tenant-scoped digest projection under
`runs.read`. That projection must resolve the selected record in the reporting
snapshot and refuse foreign tenant/result selectors. Then W1 can remove OutputRef,
its finish/report APIs and the legacy node_outputs/data_objects metadata queries
together. The physical output backends are already gone.

**W2 log client bindings remain open.** Hosts consume `blob.LogTransport` and
`logseg.SegmentRecorder`. Authenticated signing and commit routes and physical
verification are present. The client bindings and uncertain-upload verification
route still need W2's integration using `logseg.UploadVerifier`.
No substitute credential or authorization implementation was added.

**W3/W8/W9 production host composition remains external.** Supply fresh data/log
bindings per attempt and owner generation, remote-cache opt-in and source-bundle
intake. The archive proofs now exercise the real data binding that these hosts
must install. Native placement evidence remains required.

**W1 expired upload staging cleanup still needs a fenced selector.** Physical
backends can delete staged objects. Their maintenance caller needs the persisted
expired-ticket selection from W1. Log retention class and days are already sealed
by the real intake transaction in run_spec.go, so the prior report's intake
retention gap is closed.

## Shared files and assumptions

Shared production edits are limited to `pkg/controller/data.go`,
`pkg/controller/data_store.go` and `pkg/controller/data_upload_binding.go`.
`tests/acceptance/data_binding_test.go` now expects ticket authorization after
its persisted lookup. The existing storage callers migrated in
storage_test.go, storage_depcache_test.go, storage_filesystem_test.go,
storage_logs_test.go, storage_manifests_test.go and storage_s3_test.go.
The seven helper files use their former blob_* basenames with `_test.go`.
Storage source documentation was updated in `internal/blob/README.md`.

The private replica field carries the existing server identity because W1
requires trusted replica configuration at settlement. There is no new setting,
environment variable, interface, retry policy or credential policy.

Friction: the old fake signer bypassed three required integration details.
The real-binding tests exposed them and now retain their regression assertions.
The prompt's conflict list was stale. The remaining foreign seams and native
acceptance skips prevent an end-to-end completion claim.

Scratch `/tmp/sw-astra-W7` is removed at exit. Shared `/tmp/sw-gocache` is retained.

<!-- design:end -->

STATUS: CONTINUE
