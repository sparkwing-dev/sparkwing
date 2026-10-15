# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. The byte, artifact, memo, depcache, binary, source and
tagged-log components are implemented. Live output/log retirement and native
integration remain open. This continuation removes the remaining environment
artifact-backend path and adds physical filesystem acceptance properties.

## This continuation

Removed `SPARKWING_CACHE_URL` resolution, `WingdArtifactStore`, the unused
`Options.ArtifactStore` and `Backends.Artifact` fields, the local raw artifact
cache constructor, and the daemon's artifact-backend plumbing. `LocalBackends`
and `HostedBackends` no longer take an artifact backend. Every caller compiles
with the reduced signatures. No forwarding API remains. State resolution opens
only state and no longer creates an unused `cache/` directory. Its regression
uses an invalid retired URL and checks that the directory stays absent.

Removed **228 Go source/test lines**, excluding GoDoc and the acceptance runner.
Two whole test files were deleted (62 lines). Other removals include obsolete
resolver, daemon-fault and field-threading tests. Earlier commits retired 16 Go
files containing 4,147 lines and removed the cache service, package proxy, Git
mirrors, swcg grants, grant-based data routes and legacy source byte transport.

The filesystem invariant is: a signed operation cannot change tenant, key,
method, size, digest or expiry, and an interrupted commit may adopt only verified
CAS bytes. Two new generated properties exercise actual filesystem signatures,
HTTP handlers and immutable objects. They cover invalid uploads, foreign staging
keys, interrupted settlement, corrupt existing bytes, required reads and missing
objects. A fake download signer supplies the controller's already-authorized URL.
No live existence row, uploader receipt or quota settlement is inferred from
physical object success.

The log invariant is: persisted ranges and object bytes must match the requested
subject before any bytes are returned. A third property generates job and
trigger/plan/eval subjects, both retention classes and lost PUT responses. It
exercises the real batch signer, uploader, signed filesystem handler, uncertain
upload verifier and committed reader. Ordered offset reads work without a live
ring. A mismatched subject or corrupt first segment returns no bytes. Controller
authorization, lifecycle readiness and segment persistence use explicit fakes.

Three negative-control overlays bypassed signature validation, existing-CAS
verification and log-subject validation. Each corresponding property failed on
its first generated case. Repository source was never changed by the overlays.

## Acceptance and ports

`TestStorage` has **8 passing storage component checks, 424 skipped native cells
and 0 failures**. The passing component scenarios are:

- `storage-cas-integrity` (stream, physical filesystem and signature subchecks)
- `storage-reference-edge-laundering`
- `storage-memo-and-depcache-scope`
- `storage-binary-provenance`
- `storage-log-segments-and-fallback`
- `storage-owner-receipts-and-stream-verification`
- `storage-hasmany-large-budget`
- `storage-memo-materialization-failures`

Eight distinct generated properties each use 128 repeatable inputs. W6's
repository-identity component passes separately. No native target was unskipped.
New complete T1 inventory ports: **0**. Physical interrupted-upload and checksum
cases add evidence for the direct-upload replacements, but the mapped acceptance
ports still need authenticated receipt/transaction assertions. The log component
does not prove live-ring scheduling, follow, egress metering or host execution.

## Verification

Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
`GOCACHE=/tmp/sw-astra-W7/cache`, `GOTMPDIR=/tmp/sw-astra-W7/tmp` and
`TMPDIR=/tmp/sw-astra-W7/testtmp`.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Scoped `go vet -p 4` for storage packages, acceptance, orchestrator, nodehost,
  CLI and controller, including their test compilation.
- Full suites for `internal/blob/...`, `internal/artifacts`, `internal/depcache`,
  `pkg/storage/logseg`, `tests/acceptance` and `internal/acceptance`.
- Focused local state/profile resolution, dev.env and daemon display tests.
- Three negative controls failed at the intended generated assertions.
- Formatting, diff whitespace, commentcheck against HEAD and scoped sleepcheck.

The required `go run ./internal/commentcheck -base main .` fails on the existing
`internal/controllerwire/contracts.go:47` HereBearer comment. That other-area
file is unchanged. The session diff passes commentcheck.

An initial broad CLI test selection also selected two socket-dependent daemon
status tests. They could not run because Unix-socket dialing is denied by the
sandbox. The exact changed display tests pass with a narrower selection. Their
temporary socket paths were absent afterward. Full daemon/socket tests remain
with the lead. No Sparkwing binary, service, pipeline, Git write or release ran.
Scratch, including overlays and build caches, is removed at the end.

## Remaining implementation and real integration

1. W1: upload reservation/commit/abort, existence and receipts, read sets, atomic
   memo copies, unique-byte quotas, newest-success pins and reachability GC.
2. W2: `DataController.DataAuthority`, `BindData` and `client.NewDataClient` remain
   panic stubs. Bind authenticated records/data and tagged-log signing, upload
   verification, writer fencing, commits, proxy reads and egress accounting.
3. W3/W8: refresh `StorageBinding` and log bindings for each attempt/generation.
   Attach output records, artifacts, memo, depcache, binary and source clients.
   Carry tagged trigger/plan/eval subjects through the runtime.
4. W4/W9: SDK Cache/lint and CLI remote-cache/publication consumers.
5. W7: retire `pkg/controller/node_output.go`, `output_objects_s3.go`,
   `output_download_s3.go`, `log_segments.go`, `client/output.go`,
   `client/logsegments.go`, `internal/orchestrator/segment_logs.go` and legacy
   tests after cutting their live consumers to the declared contracts.
6. T1/lead: native HTTP/api.sock, SQLite/Postgres, filesystem/S3, OS/placement and
   real-repository QA. `internal/acceptance.New` and `Run` remain panic stubs.

## Shared files touched

Only obsolete artifact plumbing and its callers were changed outside W7:

- `cmd/sparkwing/wingd.go`, `cmd/sparkwing/daemon_api_report_test.go`
- `internal/nodehost/logs_test.go`
- `internal/orchestrator/backends.go`, `backends_artifact_test.go` (deleted)
- `internal/orchestrator/artifact_store_env_test.go` (deleted), `devenv_test.go`
- `internal/orchestrator/orchestrator.go`, `handle_local_trigger.go`
- `internal/orchestrator/hosted_backends.go`, `hosted_retry_test.go`
- `internal/orchestrator/profile_backend.go`, `profile_backend_test.go`
- `internal/orchestrator/wingd_api.go`, `wingd_api_harness_test.go`, `wingd_api_test.go`
- `internal/orchestrator/wingd_daemon.go`, `wingd_verb.go`
- `internal/orchestrator/wingd_admission_test.go`, `wingd_e2e_test.go`
- `internal/orchestrator/envelope_coverage_test.go`
- `internal/orchestrator/pod_node_host_test.go`, `trigger_host_args_test.go`
- `pkg/controller/doc.go`
- `tests/acceptance/storage_boundaries_test.go` invokes the added properties.

W7 additions are `tests/acceptance/storage_filesystem_test.go` and
`storage_logs_test.go`. The blob/log README files and this handoff were updated.
No contract change in another area or new product question is requested. No new
production interface, service, setting, limit, environment variable or adapter
was added.

Friction remains the unimplemented authenticated runtime and native acceptance
drivers, socket restrictions, and the unrelated commentcheck violation.
Components continue against their contract interfaces with explicit fakes.

<!-- design:end -->

STATUS: CONTINUE
