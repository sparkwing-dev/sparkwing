# W3 directory-cache continuation

<!-- design:begin -->

W3 remains incomplete. This continuation adds the executor directory-cache
layer and package-manager resolution. The [amendment handoff](w3-amendment-handoff.md)
retains the other contracts and native integration work. Earlier committed work
was retained.

## Implementation

The invariants are that archive entries stay inside the selected cache root,
outside lockfile/link targets contribute no bytes, and a mismatched upload
binding never reaches storage. Remote directory bytes use only the explicit
remote authority. They never expand the local blob client's read authority.

`DepcacheRecords.Prepare` restores once outside application retries. Lookup
receives the current authority and own repo/ref key. The record store owns
fallback ordering and provenance checks. The first readable regular lockfile
inside the acquired workspace supplies the key. That key survives retries and
lockfile changes during work. Warm directories are retained.

The archive codec uses rooted capture and the existing rooted tar extractor.
Gzip, tar, verified blob EOF and reader Close must succeed before staged files
replace the directory. Missing, corrupt, refused and over-budget reads run cold.
Partial stages are removed. Successful cold jobs save once under their final
attempt's refreshed binding. Failed jobs and empty directories save nothing.
Cache failures emit constant diagnostics and preserve the job verdict.

`SDKEnvironment.CacheDirectory` resolves Go/npm caches from captured inputs and
persisted startup/body values through procenv. Native queries use that PATH,
working directory and environment. They inherit no host credential. Explicit
directory paths retain relative and absolute support. Windows npm uses the
existing Node entrypoint policy. No customer timeout, size cap, expiry or new
environment variable was added.

An explicit `StorageBinding.RemoteCache` permits a remote lookup after a local
miss. Its returned bytes use only RemoteCache.Artifact. A profile may authorize
a remote team different from the local run's team. A remote hit causes no write.
W7 still owns verified-default provenance and live user authorization.

## Contracts, deletion and shared files

Local C3 additions are `DepcacheRecords`, its Bind/Resolve/Budget/Warn fields,
`Prepare`, and `SDKEnvironment.CacheDirectory`. No other area's contract changed.
`depcache.Key` exposes the existing pure key rule without duplicating it.
`NpmCacheCommand` replaces its private spelling, and every caller was migrated.
`tarsafe.WriteGzip` owns archive capture for both cache paths.

The old archive writer in `internal/depcache/archive.go` lost **58 lines**.
It now calls the shared codec. **0 legacy run-host lines** were deleted in this
continuation. Native `RunHosted`, `HostTriggerRun`, pod assembly and the old
nodehost scheduler/session/layers/routes still need their coordinated cut.

Shared production files are:

- `internal/depcache/archive.go`, `depcache.go` and `internal/tarsafe/write.go`.
- `internal/acceptance/host_depcache.go` and `host_executor.go`.

Shared test files are `internal/depcache/depcache_npm_test.go`,
`tests/acceptance/depcache_test.go` and `skeleton_test.go`.
The migrated private-helper callers are the original call in
`internal/depcache/depcache.go` and six test calls in `depcache_npm_test.go`.
C3, the port inventory and this report are the prose changes.

The archive codec is shared because copying the old writer would leave two
capture policies. W7 must retain the pure key, platform command and archive
helpers when deleting the old cache backend. The local layer uses the existing
byte/record contracts because no compatibility backend is authorized.
No public docs impact is claimed because native commands still use the old
assembly. C3 and its acceptance checks describe the new library surface.

## Acceptance and ports

New active cells are:

```text
TestStorage/storage-memo-and-depcache-scope/hermetic-depcache-layer
TestStorage/storage-memo-and-depcache-scope/hermetic-executor-depcache
TestStorage/storage-local-remote-cache-opt-in/hermetic-depcache-opt-in
```

The executor cell checks one restore across three attempts, final-attempt
publication, failed-job suppression and joined SDK cleanup. The remote cell
uses a team different from the local run and asserts the opt-in read boundary.
Three fuzz drivers generate archive paths/links, capture/keyfile links and
upload-binding mutations.

The new checks carry retained depcache behaviors: round trip, key stability,
warm directories, empty/missing keys, failed-job suppression and safe archive
extraction. **0 additional parked memo/materialization rows** are closed.
Native placement target children stay skipped.

## Remaining integration

Memo read/write/materialization and coalesced followers remain W3 work.
The native owner/executor assemblies must connect artifacts, depcache and memo
to the real authenticated clients, then delete the legacy runtime and tests.
W1/W2/W7/W9 still own real lookup/fallback/provenance transactions, current
upload binding and verified byte clients. Native setup should bind Resolve to
the shared SDKEnvironment.CacheDirectory method and forward safe cache warnings
to the run log. W9 still owns profile selection and remote-client construction.

The earlier full-snapshot, terminal-reference, owner-policy/approval,
cross-process-mask and selected-engine-handoff gaps remain. The prior W6 Git
environment and byte-preserving environment-digest findings remain recorded
in the snapshot report. This continuation adds no cross-area request or product
question.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W3/cache, and GOTMPDIR/TMPDIR=/tmp/sw-astra-W3/tmp.
Tests use at most -p 4.

From the worktree, with that environment:

```text
go build -p 4 ./...
go vet -p 4 ./internal/nodehost ./internal/source ./internal/lease ./internal/orchestrator ./internal/depcache ./internal/tarsafe ./internal/acceptance ./tests/acceptance
go test -p 4 ./internal/source ./internal/lease ./internal/tarsafe ./internal/acceptance ./tests/acceptance -count=1 -timeout 5m
go test -p 4 ./internal/depcache -skip '^TestRemote' -count=1 -timeout 5m
go test -p 4 -race ./internal/nodehost -run '^(TestCacheDirectory|TestSDKEnvironment|TestSDKPlanningEnvironment)' -count=1 -timeout 5m
go test -p 4 -race ./tests/acceptance -run '^(TestStorage|FuzzDepcache.*)$' -count=1 -timeout 5m
```

Run each of FuzzDepcacheArchiveConfinement, FuzzDepcacheCaptureConfinement and
FuzzDepcacheUploadBinding with `go test -p 4 ./tests/acceptance -run '^$'
-fuzz '^<name>$' -fuzztime=15s -parallel=2`. The broader focused host selection
is the amendment handoff's regex plus TestCapturedEnvironment, TestNativeSDK
and TestCacheDirectory. Use `go -C <absolute-worktree>` from any other directory.

Whole-repository build, scoped vet, the focused host suite, full source/lease,
tarsafe and acceptance suites pass. The non-HTTP depcache tests pass. New
resolver tests and storage acceptance checks pass with the race detector.
All three fuzz drivers pass 15-second runs. Archive confinement executes 2,710
cases, capture confinement 8,122, and upload binding 4,592.

The full legacy depcache suite cannot complete in this sandbox:
TestRemoteDepCacheMissSaveHitCycle panics while httptest opens its TCP listener
with `socket: operation not permitted`. The focused non-HTTP run preserves this
as unverified coverage. Commentcheck against main reports only the inherited
detached HereBearer comment in internal/controllerwire/contracts.go:47.
No W3 comment violation is reported. Git diff whitespace checks pass.

The lead retains independent review, pipelines/gates, actual HTTP/api.sock,
Postgres/S3, Windows/Darwin and native placement QA. These hermetic cells do not
claim those integrations. No Sparkwing binary, external service or git write
ran. Git configuration still has the user's default identity and no
test-injected core.worktree. Worker scratch is removed at shutdown.

<!-- design:end -->
