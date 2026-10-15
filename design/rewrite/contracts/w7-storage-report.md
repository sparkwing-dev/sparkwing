# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. This continuation starts from `1d3eaa535` with a clean
worktree. Earlier implementations and ports remain committed. This session
deletes the unused filesystem append-log backend and dead cache diagnostics,
and exercises the tagged sink on both backends. The recorded W1 layout
reproductions are unchanged and were not repeated.

## Implemented and verified

Invariant: each accepted numbered log record appears once, in writer order,
and a changed subject generation cannot select the committed bytes.

Two new acceptance cells drive concurrent writers through the real sink,
spool, compression, signed transfers, uploader, committed reader and stored
manifest. Each backend receives 128 generated cases across job, trigger, plan
and eval subjects. Duplicate delivery, newline handling, live offsets, batch
renewal, lost PUT replies, byte-offset reads and spool removal pass. The S3
signer and SDK use a fake object service. Record, authorization and lifecycle
callbacks are fakes. The new cells also pass under the race detector.

The daemon no longer carries or displays `artifact_store_error`. Its cache
backend producer was already removed. All field consumers, its wire snapshot,
CLI help, source docs and embedded copies are updated. The directly affected
JSON, display, help, snapshot and docs drift checks pass without sockets.

The earlier archive proof remains passing:

Invariant: missing, corrupt or unauthorized cache bytes never replace the
restore destination.

Those two archive cells join the signed byte store and dependency archive
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

The previous continuation changed release image fixtures to expect controller
and runner images only.
Before that change, the packaging test failed with `manifests=2`, and the latest
image test expected the removed cache image's tag to move. Its recorded fixture
passes were not repeated. Historical signed asset matrices remain untouched,
as A1-43 requires.

## Deleted

Removed `pkg/storage/fs/logs.go` (175 lines), its append test and example,
and its tests in the mixed filesystem fixtures (221 lines). That is **396
legacy implementation/test lines**. `fs.NewLogStore` had no production caller.
The raw filesystem object backend still serves legacy controller logs and
measurement. It remains until those live callers migrate.

The dead cache diagnostic cut removes another **39 code/test/snapshot lines**
and updates source docs, embedded docs and migration guidance. There is no
compatibility alias, replacement status field or new runtime mechanism.

The earlier removal of obsolete commands in `bin/pre-release-test.sh` is
committed and unchanged.

W9/lead still needs to retire or migrate that standalone script's other obsolete
package paths. The earlier report checked its syntax and exercised release
fixtures. Neither the full script nor release fixtures were run this session.

## Acceptance and ports

New passing cells:

- `TestStorage/storage-log-segments-and-fallback/filesystem-sink`
- `TestStorage/storage-log-segments-and-fallback/s3-sink`

The complete `TestStorage` run reports **72 passing test events**, including
parent summaries, and **425 skips**. There are 424 native skeleton cells and the
known real-store physical-log mismatch. These component cells do not complete
native host, PostgreSQL, Windows or Darwin evidence.

No additional parked source function is claimed ported this session. The
removed `TestAppendKeepsRecordsSeparateAcrossStores` coverage moves to the
tagged sink boundary and now checks duplicate delivery and each writer's order
on both physical backends. Earlier memo materialization, manifest authority,
upload replay, retention and cache scope ports remain in place. The archive proofs strengthen the depcache
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
| A1-53 | Two sink/backend acceptance properties and their race run pass. | T1/lead owns native drivers, platforms and real-repository QA. |

All W7 entries in `amendments-1.md` and `handoffs.md` have been read. The new
handoff records sink proofs and completed legacy cuts. Existing manifest
admission, physical log and host binding handoffs remain open with their owners. The W6/W4 timing findings are
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
- Race check for both new sink acceptance cells.
- Binary-record and source-bundle tests in `internal/bincache`.
- Wire snapshot, registry and other `pkg/wingwire` tests.
- Focused daemon JSON/display/help tests and both `pkg/docs` drift guards.
- Vet for storage, acceptance, wingwire, wingd, wingd/client, cmd/sparkwing and docs.
- `go run ./internal/commentcheck -base main .`, clean across 1101 files.
- `go run ./internal/sleepcheck -base HEAD .`, clean for this continuation.
- Formatting and diff whitespace checks.

The broader `internal/bincache` and `pkg/docs` suites cannot finish because
their HTTP fixtures require listeners. The first daemon test selection also
included socket tests and failed on Unix dialing. Each reports
`socket: operation not permitted`. The narrower storage/source, drift and
daemon report tests pass. These are sandbox limits, not passing socket evidence.

The three whole-branch sleep violations recorded in the previous report were
not rerun because those files are unchanged. Pipelines, native platforms,
PostgreSQL, service sockets, installed binaries and real registries remain
unexecuted. The lead owns those lanes and independent review.

## Shared files and friction

Shared source/caller edits are `cmd/sparkwing/daemon.go`,
`cmd/sparkwing/help_registry.go`, `internal/wingd/wingd.go`,
`internal/wingd/server.go`, `internal/wingd/client/probe.go`, and
`pkg/wingwire/messages.go`. They only remove the dead cache diagnostic.
Its fixture edits are `cmd/sparkwing/daemon_api_report_test.go` and
`pkg/wingwire/testdata/shapes.json`.

Shared acceptance edits are `tests/acceptance/storage_test.go`,
`tests/acceptance/storage_logs_test.go` and new
`tests/acceptance/storage_log_sink_test.go`.

Shared docs are `CHANGELOG.md`, `pkg/docs/changelog.md`, `docs/cli-daemon.md`,
`pkg/docs/mirror/cli-daemon.md`, `docs/local-execution.md`,
`pkg/docs/mirror/local-execution.md`, `docs/migrations/_unreleased.md`,
`pkg/docs/mirror/migrations/_unreleased.md`,
`design/rewrite/contracts/handoffs.md` and this report. W7's two READMEs are
also updated. No frozen rewrite contract changed.

Friction remains W1's result/log layout, W2's unimplemented authenticated
bindings and the sandbox's refusal of socket fixtures. There is no new contract
change, product question or decision for the lead. No new runtime mechanism,
customer limit, setting or environment variable was introduced. The supplied
Xwing environment was reused. No Git write, Sparkwing binary, pipeline,
deployment or release ran. Scratch
is deleted before the final report.

<!-- design:end -->

STATUS: CONTINUE
