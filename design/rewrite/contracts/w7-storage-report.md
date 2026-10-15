# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. The byte, artifact, memo, depcache, binary, source and
tagged-log components are implemented against the amended contracts. Live
legacy output/log paths and native integration remain open. This continuation
adds storage component acceptance evidence and removes two gitcache leftovers.

## This continuation

The storage invariant is: only verified bytes and an exact uploader receipt or
an authorized read edge may create a reachable reference.

Seven `TestStorage/<scenario>/storage-components` checks now execute:

| Scenario | Component evidence |
| --- | --- |
| storage-cas-integrity | Generated intact, same-size corrupt, short and long streams; exact-length reads closed before EOF remain unverified. |
| storage-reference-edge-laundering | Generated receipt mutations across team/run/job/attempt/generation/principal/capability; matching declared read edges work and another reader's edges fail. |
| storage-memo-and-depcache-scope | Generated Unicode components containing traversal, slash, percent, backslash and manual-ref syntax cannot collapse component boundaries or manual/verified namespaces. |
| storage-binary-provenance | Generated digest/tenant/provenance/trust combinations; local bytes need team opt-in and unknown provenance never gains execution authority. |
| storage-owner-receipts-and-stream-verification | Trigger/plan/eval upload identities retain their unit fences, may upload binary/source bytes, and cannot grant unrelated job output authority; verified-stream property also runs. |
| storage-hasmany-large-budget | All 4,097 distinct requested digests reach the signing contract, duplicates collapse, its independently supplied readable results survive, and expiry/cancellation/unsolicited results return no partial success. |
| storage-memo-materialization-failures | A missing result, refused record copy, missing/corrupt/short bytes or members without a manifest yields a miss; a verified result and explicit empty output yield hits. |

Five distinct property checks each generate 128 inputs using a repeatable seed.
Signing, transfer and record persistence are fakes where the owning area remains
a stub. Tests exercise the real W7 policy and verification functions. Four
negative-control overlays weakened receipt identity, namespace escaping,
verified EOF and binary trust separately. Each made its corresponding component
check fail. The overlays never changed repository source and are removed with
scratch.

`TestStorage` has **7 passing storage component checks, 424 skipped native cells
and 0 failures**. W6's repository-identity component still passes separately.
These checks do not prove transactions, HTTP/api.sock, physical backends, host
execution, placement or event counts. No native target was unskipped. New
complete T1 inventory ports: **0**. Remaining inventory findings still need the
transactional and host acceptance drivers.

Removed `sourceurl.ClaimedRepoNameFromURL`, which had no production caller,
its helper-only test, and the obsolete cache chart values from the Kubernetes
fixture. The controller test now uses a fixed retired-path fixture and preserves
its existing 403 assertion. **58 lines removed, 1 fixture line added, 0 files
deleted**. This includes 17 source lines, 27 source-test lines, 3 controller-test
lines and 11 shell lines removed. No compatibility API was added.

Earlier continuations retired 16 Go files containing 4,147 lines and removed the
cache service, package proxy, Git mirrors, swcg grants, grant-based data routes,
cache counters and legacy source byte transport. Earlier artifact/archive,
receipt, cancellation and cleanup fixes remain intact.

## Verification

All Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`
and caches/temporary directories beneath `/tmp/sw-astra-W7`.

Passed:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4` for `internal/blob/...`, `internal/artifacts`,
  `internal/depcache`, `pkg/storage/logseg`, `internal/sourceurl` and
  `tests/acceptance`.
- Full test suites for those packages, including the seven new component checks.
- Controller test compilation with `go test -p 4 ./pkg/controller -run '^$'`.
- `bash -n bin/k8s-e2e.sh`, formatting, scoped commentcheck and sleepcheck.

Scoped lint reports no issue in the changed files. Its package run still fails
on the existing `tests/acceptance/credential_routes_test.go:69` raw `POST`
literal (`usestdlibvars`). That other-area file is unchanged.

The native T1 `New` and `Run` drivers remain panic stubs. No Sparkwing binary,
service, pipeline, deployment or release was run. The lead owns pipelines,
independent review, staging, commits and landing. The worktree was clean and
had no merge conflicts at entry; earlier report references to an unmerged index
are obsolete. Scratch and the worktree-local lint cache are deleted at the end.

## Remaining implementation and real integration

1. W1: upload reservation/commit/abort, existence and receipts, read sets, atomic
   memo copies, unique-byte quotas, newest-success pins and reachability GC.
2. W2: `DataController.DataAuthority`, `BindData` and `client.NewDataClient` are
   still panic stubs. Bind authenticated records/data and tagged-log signing,
   verification, writer fencing, commits, proxy reads and egress accounting.
3. W3/W8: refresh storage/log bindings for each attempt and generation. Attach
   output records, artifacts, memo, depcache, binary and source clients. Carry
   tagged trigger/plan/eval subjects through the runtime.
4. W4/W9: SDK Cache/lint and CLI remote-cache/publication consumers.
5. W7: retire `pkg/controller/node_output.go`, `output_objects_s3.go`,
   `output_download_s3.go`, `log_segments.go`, `client/output.go`,
   `client/logsegments.go`, `internal/orchestrator/segment_logs.go` and their
   legacy tests after cutting their live consumers over to the declared
   byte/record and tagged-log contracts. These paths have live callers; deleting
   only their handlers would strand runtime consumers.
6. T1/lead: real HTTP/api.sock, SQLite/Postgres, filesystem/S3, OS/placement and
   real-repository QA. No component result above closes those obligations.

## Shared paths and decisions

Shared paths touched in this continuation:

- `internal/sourceurl/{sourceurl.go,sourceurl_test.go}`: removed dead gitcache API.
- `pkg/controller/declared_repo_grants_nothing_test.go`: migrated its only caller.
- `bin/k8s-e2e.sh`: removed retired cache-chart values.
- `tests/acceptance/skeleton_test.go`: invokes W7 component checks separately from
  native targets.

W7 additions are `tests/acceptance/storage_boundaries_test.go`, the blob README
coverage note and this handoff. No contract change in another area or new
product question is requested. No production mechanism, service, setting,
limit, environment variable or compatibility adapter was added.

Friction: the shared native acceptance and authenticated runtime adapters remain
unimplemented, and the brief reserves socket-dependent pipelines for the lead.
Components continue against their contract interfaces and use explicit fakes.

<!-- design:end -->

STATUS: CONTINUE
