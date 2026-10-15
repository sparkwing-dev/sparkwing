# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. The continuation started at committed `8db9fed4a` with a
clean worktree. Earlier merge resolutions and storage implementations are intact.
No Git write, Sparkwing binary, pipeline or service ran.

## Implemented in this continuation

Invariant: signing, verification, persistence and reads must identify the same
physical object for an exact tagged subject and slot. Distinct tenants, runs,
job attempts, owner units and generation fences must never share a key.

`internal/blob/log_keys.go` now owns physical log namespaces and filenames.
`LogObjectKey(subject, class, index)` composes the validated identity with zero
for the final manifest and positive segment slots. W1 can import this owner
without importing `logseg` or creating a store cycle. Its generated test varies
all four subject kinds, tenant, run, job, attempt, unit/source generations,
retention and segment numbers across 128 inputs. Invalid paths and noncanonical
segment names are refused.

The old `logseg` key APIs are deleted. Signing, uncertain-PUT verification,
uploading, reading, retention and their tests call the common owner directly.
The physical filesystem parser uses that same segment-number parser. There is
no forwarding alias or old-key fallback. Existing physical key strings stay the
same. No customer limit or environment variable was added.

Deleted **118 lines** from the old `logseg/keys.go`, **8 lines** of duplicate
filesystem parsing and **3 lines** of duplicate replay key selection. Most key
logic moved to the common owner. This is not a net code reduction. No additional
legacy service or controller output/log subsystem was deleted in this session.

## Physical log integration and contract changes needed

`CheckBlobLogRecords` joins real signed filesystem transfers and the real
`SubjectUploader` to SQLite `Tenant.RecordSegments`. Its intended remainder
reopens the store, verifies offset reads and exact retained bytes, then revokes
the reader and requires refusal before emitting bytes. Signing/HTTP dispatch is
a socket-free contract fixture. Record authorization and persistence are real.

The first executed case failed with:

```
persist physical segment 1: log segment object belongs to another signing slot
```

W1's `LogSegmentObjectKey` derives
`teams/<team>/logs/<class>/<run>/<subject-hash>/<index>`, and `appendSegmentsTx`
starts at zero. W7 signs `logs/<class>/teams/<team>/runs/<run>/.../seg-000001.ndjson.gz`.
The signed transport already reserves index zero for the manifest. The record-slot
contract needs agreement with W1. Recommended integration: use `blob.LogObjectKey`,
start persisted segment indexes at one and port the zero-based store fixtures.
No second physical layout or compatibility path is needed. W1 implementation
and its record types were not changed. The explicit acceptance skip names this
blocker. Remove it after the real boundary passes. The W1 handoff contains the
reproduction command.

The C1 documentation records W7's new key helpers and signed-slot semantics.
No other area's Go contract shape changed. The earlier W1 output/manifest/memo
layout and repo-reader selector requests remain open. W2 still needs the
production log/data transports. There is no new product question.

## Acceptance, ports and verification

`TestStorage` passes **45 leaf checks**, with **64 passing test events**.
**424 native cells remain skipped**, plus the new blocked filesystem-records
cell. The new cell's initial red run is failure evidence, not acceptance coverage.
There are no new passing native cells or completed native T1 inventory ports.

The retained key-layout, tagged-subject, signed-batch, uncertain-PUT and committed
reader tests were migrated to the common API. Their assertions remain in place.
Two new key tests cover generated identity separation and invalid slots/paths.
No old port-inventory finding is marked closed by a fake controller.

Checks use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
`GOCACHE=/tmp/sw-astra-W7/cache`, `GOTMPDIR=/tmp/sw-astra-W7/gotmp` and
`TMPDIR=/tmp/sw-astra-W7/tmp`.

Passed:

- Root `go build -p 4 ./...` and compile-only tests for all 172 root packages.
- Full tests for blob, blob/depcache, dependency caches, artifacts and logseg.
- Focused key, subject, batch-signing and storage acceptance tests.
- Vet for those packages, acceptance, controller and controller/client.
- Commentcheck is clean across 1085 files.
- Source/embedded documentation drift, formatting and diff whitespace checks.
- Focused embedded-docs guard and migration tests.

The lead still owns pipeline gates, independent review and native HTTP/api.sock,
S3/Postgres, OS and placement evidence. Prior W3 runtime-test failures and Git
fixture friction were not rerun in this continuation.

## Amendment and handoff ledger

| Entry | W7 component status | Real integration left |
| --- | --- | --- |
| A1-24 | Byte/manifest owner adapters implemented. | W2 binds W1's checker and verified manifest reader. |
| A1-29 | Restricted verified-default reads retain authorized member metadata. | W1 resolves repo-reader tenant/default-ref selectors. |
| A1-30 | RemoteCacheOptions.Records and logical cache reads implemented. | W2/W9 construct authenticated profile transports. |
| A1-31 | Exact job, owner-unit and publication upload identities checked. | W2 derives current identity at authentication. |
| A1-32 | Physical upload/receipt/settlement boundary proofs implemented. | W2 binds reservation, reauthorization, physical commit and replay. |
| A1-33 | Filesystem/S3 ObjectBackend, collection and verified manifests implemented. | W2 and maintenance bind the physical owner. |
| A1-34 | Signed filesystem/S3 byte components implemented. | DataAuthority, BindData and NewDataClient remain stubs. |
| A1-35 | Output, memo, depcache, staging, collection and tagged-log components implemented. Physical log keys have a neutral owner. | W1 output/memo layout and physical log-slot alignment remain open. |
| A1-36 | Artifact, memo and depcache layers use common byte/record callbacks. Cache writes refresh after execution. | W3/W8 construct fresh StorageBinding values per attempt/generation. |
| A1-37 | SDK/host keys share contained reads and go-modules resolver. Removed lint transport stays deleted. | W3/W8 finish production hosts. |
| A1-43 | Cache deletion preserves engine/releaseasset ownership. | W8 version-aware asset functions remain stubs. |
| A1-50 | Read-only remote-cache, depcache and source/binary byte components implemented. | W9 wires profile opt-in, publication and engine clients. |
| A1-53 | Component and physical/store proofs remain passing where aligned. New log mismatch reproduced. | T1/lead owns native drivers and real-repository QA. |

The existing W7 manifest admission, W1 record-layout and W3 storage-layer
handoffs remain open in their respective areas. The new physical-log handoff
supplies W7's shared API and a compiled real-store proof. Other entries in
handoffs.md name W3/W4/W5/W8/W9 and were left to those owners.

## Remaining deletion and integration

The old controller output/log handlers and their consumers still need the final
bindings: `node_output.go`, `output_objects_s3.go`, `output_download_s3.go`,
`log_segments.go`, client `output.go` and `logsegments.go`, orchestrator
`segment_logs.go`, their tests and raw artifact/log backends. Their replacements
must use authenticated data/log bindings and the shared state owner. W7 did not
replace other owners' constructors or introduce a compatibility transport.

W2 must bind tagged logs, future-slot admission, uncertain-PUT verification,
record commits, proxied reads and exact egress accounting. Maintenance must bind
reachability collection and filesystem log expiry. W3/W8 need final per-attempt
and owner-unit bindings. W9 needs publication and explicit remote-cache profiles.
These are real integration obligations beyond the existing contract fakes.

## Shared files and friction

The removed key APIs required small caller migrations in controller
`client/logsegments.go`, `deletion.go`, `log_reads.go`, `log_retention.go` and
`log_segments.go`, plus `deletion_review_test.go` and
`log_segments_internal_test.go`. These edits change key-owner calls/imports only.
No other area's persistence implementation was edited.

Other shared edits are the acceptance checker/registration, C1/handoffs/report,
CHANGELOG.md, storage/migration source docs and their generated embedded copies.
The remaining source/test edits are in W7's blob/logseg ownership.

Friction: W1's logical records and W7's signed objects each passed their isolated
proofs while using incompatible keys and first indexes. The new real-store cell
reproduces that gap. No cross-area contract fix was applied silently. The supplied
Xwing 1084 worktree was reused. Scratch is removed before the final response.

The full docs test command could not complete because its WebClient fixture
needs a socket. `httptest.NewServer` failed with `socket: operation not permitted`.
The source/embedded drift check passed separately.

<!-- design:end -->

STATUS: CONTINUE
