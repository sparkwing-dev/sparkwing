# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. This continuation starts from `461a433f5` with a clean
worktree. Earlier storage implementations and ports remain committed. This
session adds archive integration evidence and finishes stale cache delivery
references. It does not repeat the recorded result/log-layout reproductions.

## Implemented and verified

Invariant: missing, corrupt or unauthorized cache bytes never replace the
restore destination.

Two new acceptance cells join the signed byte store and dependency archive
backend to real SQLite upload reservations, immutable physical publication,
receipt settlement and record transactions. Each backend receives 128 generated
payloads. The fixture closes and reopens SQLite before restoring. Forged
repository, ref and attempt selectors fail. Equal-length corruption and missing
objects leave destination files intact. Corrupt restores run cold. Failed work
leaves the cache record unchanged. Successful cold work publishes a replacement
archive that restores correctly.

The filesystem proof uses real files and signed HTTP handlers. The S3 proof
uses the real signer and SDK with a fake object service. Authentication and
claim issuance are seeded. The signing/commit adapter is a contract fake over
the real store APIs. Native controller routes and host construction remain open.
No production storage interface or runtime mechanism was added.

The release image fixtures now expect controller and runner images only.
Before the change, the packaging test failed with `manifests=2`, and the latest
image test expected the removed cache image's tag to move. Both tests now pass
against the current scripts and a fake registry. Historical signed asset
matrices remain untouched, as A1-43 requires.

## Deleted

Removed three commands targeting the deleted Git cache package from
`bin/pre-release-test.sh`. The code diff removes **6 lines**: three obsolete
shell commands and three replaced test expectations. No legacy runtime storage
API was removed in this continuation. The new acceptance driver and property
wrappers total 349 lines. Their scope is test evidence.

W9/lead still needs to retire or migrate that standalone script's other obsolete
package paths. This session checks its syntax only and does not claim that the
full script runs. Current release scripts were exercised through the fixtures.

## Acceptance and ports

New passing cells:

- `TestStorage/storage-memo-and-depcache-scope/filesystem-archives`
- `TestStorage/storage-memo-and-depcache-scope/s3-archives`

The complete `TestStorage` run reports **70 passing test events**, including
parent summaries, and **425 skips**. There are 424 native skeleton cells and the
known real-store physical-log mismatch. These archive cells do not complete
native host, PostgreSQL, Windows or Darwin evidence.

No additional parked source function is claimed ported this session. Earlier
memo materialization, manifest authority, upload replay, retention and cache
scope ports remain in place. The new archive proofs strengthen the depcache
cold-start and recovery requirements through real physical/store boundaries.
Host transition/event ports still belong to W1/W3/W8.

## Amendment and handoff ledger

| Entry | W7 status | Integration left |
| --- | --- | --- |
| A1-24 | Physical byte and verified manifest adapters implemented. | W2 installs W1's transactional checker and manifest reader. |
| A1-29 | Restricted reads retain authorized member metadata. | W1 resolves repo-reader tenant/default-ref selectors. |
| A1-30 | Logical remote-cache transport interface and client implemented. | W2/W9 construct authenticated profile transports. |
| A1-31 | Tagged job, owner and publication upload identities checked. | W2 derives the binding from current authentication. |
| A1-32 | Filesystem/S3 upload, receipt and replay proofs implemented. Archive uploads now also reach real reservation/settlement transactions. | W2 binds the authenticated lifecycle and receipt replay. |
| A1-33 | Physical owner, collection, manifest reads and measurements implemented. | W2/maintenance bind collection and log expiry. |
| A1-34 | Signed filesystem/S3 components implemented. | DataAuthority, BindData and NewDataClient remain stubs. |
| A1-35 | Output, memo, depcache, staging, collection and tagged-log components implemented. New archive/store proofs pass. | W1 aligns C1 result/memo keys and physical log slots. |
| A1-36 | Artifact, memo and depcache layers use common callbacks and refresh after execution. | W3/W8 construct fresh StorageBinding values per attempt/generation. |
| A1-37 | Shared contained keys and go-modules resolver implemented. Removed lint transport stays deleted. | W3/W8 finish production host construction. |
| A1-43 | Cache-service cut retains historical release assets. Current image fixtures now match the two-image release scripts. | W8 finishes version-aware asset functions. |
| A1-50 | Read-only cache and publication byte components implemented. | W9 wires explicit profile opt-in, publication and engine clients. |
| A1-53 | Two more physical/store acceptance cells pass. | T1/lead owns native drivers, platforms and real-repository QA. |

All W7 entries in `handoffs.md` have been read. The new handoff records archive
proofs and delivery cleanup. Existing manifest admission, physical log and host
binding handoffs remain open with their owners. The W6/W4 timing findings are
unchanged. No new contract change or product decision is requested.

## Real integration and remaining deletion

W1's `ResultBlobKey` uses `Name=result` and a run/attempt/generation scope key.
C1 requires the run ID as name and an empty scope key. W1 memo persistence still
selects `output`/`manifest`, while the contract uses `MemoRecordName=result`.
W1 log records use a different physical key and zero-based indexes. W7's shared
physical owner reserves zero for the manifest and starts segments at one.
The executed failures and skipped SQLite log proof remain in `handoffs.md`.

W2 must implement and bind `DataController.DataAuthority`, `BindData` and
`client.NewDataClient`, including authenticated log transport, reservation,
settlement and exact egress. W3/W8 must construct fresh storage/log bindings.
W9 must bind explicit remote-cache and publication profiles. Maintenance must
bind reachability collection and filesystem log expiry.

After those bindings, delete controller `node_output.go`, `output_objects_s3.go`,
`output_download_s3.go`, `log_segments.go`, client `output.go`/`logsegments.go`,
orchestrator `segment_logs.go`, their tests and the raw artifact/log backends.
Those files still have live callers. Replacing them with compatibility
transports would violate the hard cut. W7's replacements remain testable through
the declared interfaces while those integrations are open.

## Verification

Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, at most
`-p 4`, and `GOCACHE`, `GOTMPDIR` and `TMPDIR` under `/tmp/sw-astra-W7/`.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Full tests for `internal/blob/...`, `internal/artifacts`, `internal/depcache`
  and `pkg/storage/...`.
- Complete `TestStorage` acceptance run.
- Both release-image tests in `./bin`, including the retag cases.
- Vet for storage, internal/acceptance, tests/acceptance and bin packages.
- `go run ./internal/commentcheck -base main .`, clean across 1095 files.
- `go run ./internal/sleepcheck -base HEAD .`, clean for this continuation.
- Shell syntax, formatting and diff whitespace checks.

The three whole-branch sleep violations recorded in the previous report were
not rerun because those files are unchanged. Pipelines, native platforms,
PostgreSQL, service sockets, installed binaries and real registries remain
unexecuted. The lead owns those lanes and independent review.

## Shared files and friction

Shared edits are `bin/release_images_test.go`, `bin/pre-release-test.sh`,
`tests/acceptance/storage_test.go` and new
`tests/acceptance/storage_depcache_test.go` and
`internal/acceptance/blob_depcache.go`. W7's README and the handoff/report files
are updated. No callers outside these fixtures were migrated.

Friction remains W1's result/log layout and W2's unimplemented authenticated
bindings. No new runtime mechanism, customer limit, setting or environment
variable was introduced. The supplied Xwing environment was reused. No Git
write, Sparkwing binary, pipeline, service, deployment or release ran. Scratch
is deleted before the final report.

<!-- design:end -->

STATUS: CONTINUE
