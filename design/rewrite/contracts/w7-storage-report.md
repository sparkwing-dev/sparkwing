# W7 storage handoff

<!-- design:begin -->

## Latest continuation

Started at `83732aab8` with a clean worktree and no merge in progress.
Earlier W7 implementations and deletions remain committed. The W7 entries in
`amendments-1.md` and `handoffs.md` were checked. No later amendments file exists
on this branch. No new W7 runtime stub or contract repair was found.

Added publication component properties for filesystem and S3. Each backend
receives 128 generated imported attempts and payloads, including empty output.
They join `OutputData`, artifact capture, signed upload, physical immutable
publication, verified output reads and artifact staging. Ten forged selectors
per case cover team, run, job, attempt, generation, principal, capability,
missing imported attempt, changed identity kind and missing publication binding.
All fail before byte upload or record commit. Refused output and manifest
updates return errors and preserve prior records, readable output and local files.

Invariant: only the bound imported attempt can publish output or artifact bytes,
and a refused record update cannot replace an earlier committed result.

The signer and record callbacks are contract fakes. The byte paths use real
filesystem handlers or the real S3 signer and SDK with a fake object service.
These checks do not establish controller intake, local provenance,
`trust_local_builds`, terminal transitions, published logs or CLI exit behavior.
Those remain integration work for W1/W2/W3/W9. No runtime API or contract changed.

## Deleted and ported

Deleted **0 runtime lines** in this continuation. The remaining legacy output
and log paths have live controller, host and CLI callers. Their final bindings
are still owned by other areas as listed below. Earlier cache-service,
filesystem append-log and raw-factory cuts were not repeated.

No new parked source test is claimed ported. The existing storage ports still
pass. The two new cells cover the byte/record portion of publication using the
final contract instead of invoking the legacy publication path.

## Acceptance and verification

New passing cells:

- `TestStorage/storage-publish-one-way/filesystem-components`
- `TestStorage/storage-publish-one-way/s3-components`

Both pass with race detection. Full `TestStorage` reports **76 passing test
events and 425 skips**. The skips remain 424 native skeleton cells and W1's
known physical log-slot mismatch. No native matrix cell was reclassified.

Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W7/cache`, `GOTMPDIR=/tmp/sw-astra-W7/gotmp` and
`TMPDIR=/tmp/sw-astra-W7/tmp`. Tests use at most `-p 4`.

Passed:

- `go build -p 4 ./...` for the whole repository.
- `go test -p 4 ./internal/blob/... ./internal/artifacts ./internal/depcache ./pkg/storage/...`.
- `go test -json -p 4 ./tests/acceptance -run '^TestStorage$' -count=1`.
- `go test -race -p 4 ./tests/acceptance -run '^TestStorage/storage-publish-one-way/(filesystem|s3)-components$' -count=1`.
- `go vet -p 4` for the storage packages above and `./tests/acceptance`.
- `TestPkgDocsContentMatchesDocsRoot` and `TestEmbeddedChangelogMatchesRoot`.
- `go run ./internal/commentcheck -base main .`.
- `go run ./internal/sleepcheck -base HEAD .`.
- Formatting and diff whitespace checks.

The first publication run refused the fixture because it omitted repository/ref
scope. Correcting the fixture made both backend cells pass. No product defect
or runtime fix is claimed from that failure.

The supplied Xwing environment was reused. No Sparkwing binary, pipeline,
Git write, deployment or external service ran. Scratch is deleted before the
final response. The lead retains independent review, committing and native QA.
There is no adopter-facing behavior change or changelog impact.

## Amendment and handoff ledger

| Entry | W7 status | Integration left |
| --- | --- | --- |
| A1-24 | Physical byte and verified manifest adapters implemented. | W2 installs W1's transactional checker and manifest reader. |
| A1-29 | Restricted reads retain authorized member metadata. | W1 resolves repo-reader tenant/default-ref selectors. |
| A1-30 | Logical remote-cache transport and ref-free read selectors implemented. | W2/W9 construct authenticated profile transports. |
| A1-31 | Tagged job, owner and publication upload identities checked. | W2 derives the binding from current authentication. |
| A1-32 | Filesystem/S3 upload, receipt and replay proofs implemented. Archive uploads now also reach real reservation/settlement transactions. | W2 binds the authenticated lifecycle and receipt replay. |
| A1-33 | Physical owner, collection, manifest reads and measurements implemented. | W2/maintenance bind collection and log expiry. |
| A1-34 | Signed filesystem/S3 components implemented. | DataAuthority, BindData and NewDataClient remain stubs. |
| A1-35 | Output, memo, depcache, staging, collection and tagged-log components implemented. New archive/store proofs pass. | W1 aligns C1 result/memo keys and physical log slots. |
| A1-36 | Artifact, memo and depcache layers use common callbacks and refresh after execution. | W3/W8 construct fresh StorageBinding values per attempt/generation. |
| A1-37 | Shared contained keys and go-modules resolver implemented. Removed lint transport stays deleted. | W3/W8 finish production host construction. |
| A1-43 | Cache-service cut retains historical release assets. Current image fixtures now match the two-image release scripts. | W8 finishes version-aware asset functions. |
| A1-50 | Read-only cache and publication byte components implemented. | W9 wires explicit profile opt-in, publication and engine clients. |
| A1-53 | Two sink/backend acceptance properties and their race run pass. | T1/lead owns native drivers, platforms and real-repository QA. |

## Real integration and remaining deletion

W1's `ResultBlobKey` still uses `Name=result` and a run/attempt/generation
scope key. C1 requires the run ID as name and an empty scope key. W1 memo
persistence still uses `output`/`manifest` instead of `MemoRecordName=result`.
Its physical log key differs from `blob.LogObjectKey`, and its records start
at zero while signed segment indexes start at one. These implementations were
rechecked. Earlier executed failures and the explicitly skipped SQLite log
proof remain recorded in `handoffs.md`.

W2's `DataController.DataAuthority`, `BindData` and `client.NewDataClient` still
panic as unimplemented. W2 must bind authenticated records, signing, upload
reservation/settlement/replay, log transport and exact egress. W1 must resolve
repo-bound readers' tenant/default ref. W3/W8 must construct fresh storage and
log bindings per attempt/generation. W9 must install explicit remote-cache and
publication profiles. Maintenance must bind physical collection and filesystem
log expiry.

After those bindings, delete controller `node_output.go`, `output_objects_s3.go`,
`output_download_s3.go`, `log_segments.go`, client `output.go`/`logsegments.go`,
orchestrator `segment_logs.go`, their tests and the raw artifact/log backends.
Those files still have live callers. No replacement compatibility transport was
added. W7 remains incomplete while these real integration and deletion points
are open.

## Shared files and friction

Shared acceptance edits are `tests/acceptance/storage_test.go`,
`storage_materialization_test.go` and new `storage_publication_test.go`.
The shared record fake now supports a refused commit without altering earlier
materialization cases. Shared docs are `handoffs.md` and this report.
The W7-owned `internal/blob/README.md` describes the component evidence.
No caller in another runtime package changed.

No cross-area contract change or open product question is requested. No runtime
mechanism, limit, setting, dependency or environment variable was added.
The remaining friction is W1's result/memo/log layouts and W2's unimplemented
bindings. All W7-owned contract components have implementations and fake-backed
proofs. W7 remains incomplete until real bindings replace the live legacy paths.

<!-- design:end -->

STATUS: CONTINUE
