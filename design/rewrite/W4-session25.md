# W4 continuation report

Continues [session 24](W4-session24.md). The amendment work and earlier hard cuts
remain in place. The ten merge paths already had resolved contents on entry.
No conflict markers remain. Their Git index entries are still unmerged because
the swarm brief forbids Git writes. The lead must stage them and finish the merge.

The public fake host now rejects an empty successful OIDC callback result before
mask registration. The real node-host transport already requires a nonempty
token. A new acceptance regression reproduced the false success, then passed
after the three-line fixture correction. This changes no public signature,
setting, limit or production transport behavior.

Deleted legacy code this session: **0 lines**. No inventory test needed another
port. The new regression uses the existing public fake-host acceptance seam.

## Verification

Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W4/cache` and `GOTMPDIR`/`TMPDIR=/tmp/sw-astra-W4/tmp`.
Each command used `go -C /home/bombuntu/.xwing/envs/1082/sparkwing`.

- `build -p 4 ./...` passed after the change.
- `vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...` passed after the change.
- W4 package tests passed before the three-line correction with `-p 4 -count=1 -timeout 90s` and the seven inherited exclusions below. The changed fake-host package also passed after the correction with `-count=1 -timeout 60s`.
- `test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v -timeout 90s` passed after the change.
- Formatting and `git diff --check` passed.

The three selected Linux Go hello/handles, version/hard-cut and fake-host/linkage
targets pass. Fake-host/linkage includes the new `empty-oidc-token` regression.
Its HTTP subprocess subtest remains socket-blocked. Mask's SDK service subtest
passes, while the full mask target remains skipped for the real placement driver.
No additional full acceptance cell was promoted.

The package command covered `./internal/nodeproto/... ./internal/protofd
./internal/sdkserve ./sparkwing/...`. Its inherited `-skip` expression was
`^(TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$`.
Three filesystem cases encounter the sandbox's ancestor `/tmp/.git`, and four
service-port cases require sockets. Those failures were not retried. No new
concurrency path was added, so this session did not repeat the earlier race suites.

## Remaining integration

- W2/W3 must mount and authorize mask, annotation and summary routes and install
  sink-wide acknowledgement. `internal/controller/policy_contract.go` still has panic
  stubs. W4 transport behavior remains exercised with RoundTripper fakes.
- W6 must implement `settings.ResolveConcurrency`, which still has a panic stub.
  W4 preserves omission and tests structural validation against that interface.
- W3/W1 own persisted expansion/replay checks and storage authority. W3/W8 own
  exact engine and granted identity delivery for remote placements.
- W5 owns consumption of the shared declaration/error vectors. W9/lead own the
  remaining pipeline runtime-test migration and generated public references.
- T1/lead must supply native Windows/Darwin, real HTTP/api.sock, remote placement,
  store/backend and full-matrix evidence. This worker cannot provide those drivers
  by extending SDK fakes.

No new cross-area contract change or design question is requested. These are
implementation and evidence handoffs, not requests to change the architecture.

## Files and friction

Production fixture change: `sparkwing/sparkwingtest/host.go`.

Shared files touched this session:

- `tests/acceptance/protocol_test.go`
- `tests/acceptance/protocol_amendment_test.go`
- `design/rewrite/contracts/c3-go-sdk-notes.md`
- This report.

No removed API needed a caller migration this session. The inherited merge and
caller-migration file lists remain in [session 23](W4-session23.md).

Friction remains denied sockets, the ancestor `/tmp/.git`, the stale Overwing
binary and inherited nested-pipeline runtime-test compilation failures. Those
pipeline failures were not rerun. One installed read-only `sparkwing info
--for-agent` discovery call ran during bootstrap before the swarm brief was
loaded. No stateful Sparkwing execution, candidate execution or Git write ran.
Git still names Korey and has no `core.worktree` override. All started checks
finished. Scratch and build caches under `/tmp/sw-astra-W4/` were removed.

STATUS: CONTINUE
