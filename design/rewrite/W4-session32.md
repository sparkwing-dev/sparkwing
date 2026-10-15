Continues [session 31](W4-session31.md) from committed HEAD `2fb59e995`.
The worktree was clean on entry. This continuation changes only this report.

W4's implementation and previous hard cuts remain intact. The owned production
packages contain no unimplemented marker or Old-suffixed API. No new W4
implementation gap was established. Legacy code deleted: **0 lines**. New ported
inventory tests: **0**. No caller in another package needed migration.

The whole-repository build, W4 vet, available package tests and selected Linux
acceptance checks pass. All Go commands used `GOWORK=off`,
`GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W4/cache`, and `GOTMPDIR`/`TMPDIR=/tmp/sw-astra-W4/tmp`,
with `go -C /home/bombuntu/.xwing/envs/1082/sparkwing`.

Checks passed:

- `build -p 4 ./...`.
- `vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...`.
- `test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -count=1 -timeout 120s`, with the seven inherited exclusions below.
- `test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v -timeout 120s`.

The package exclusions were
`^(TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$`.
An additional focused run of the three fileset cases with
`GIT_CEILING_DIRECTORIES=/tmp/sw-astra-W4/tmp` failed. `listGitFiles` walks
ancestor `.git` paths itself, reaches the sandbox's empty `/tmp/.git`, and then
Git refuses that directory. Bounding Git's discovery does not change the
helper's search. This confirms the existing exclusion rather than clearing it.
The four socket cases retain their previous environment limitation.

The selected acceptance run passed hello/handles, version/hard-cut and the
available fake-host/linkage subtests. Its real HTTP host-route subtest skipped
because the sandbox denies sockets. The mask SDK-service subtest passed, while
the full mask cell skipped for W2/W3's route and sink integration. No new full
placement or native-platform cell is claimed.

Real integrations still required:

- W2/W3 must connect authenticated mask, annotation and summary routes to host
  sinks and the shared secret matcher. `PolicyRouter` remains a panic stub.
- W6 must resolve omitted concurrency fields and seal the winners.
  `ResolveConcurrency` remains a panic stub.
- W3/W1 must exercise persisted expansion, code replay and drift enforcement,
  plus artifact, memo and depcache authority through real host/store boundaries.
- W3/W8 must deliver verified engines and granted runner identities.
  `Resolved.InstallSDK` remains a panic stub.
- W5 must consume the shared v2 corpus. W9 owns consumer documentation and
  generated references. W3/W8 own runtime fixture retirement.
- T1/lead must supply the other placement drivers, native Windows/Darwin and
  backend evidence, and full real-pipeline QA.

The existing W4 fakes cover the declared SDK service boundaries. Further W4
implementation should follow a concrete integration failure or changed contract.
There is no new cross-area contract request, open design question, runtime
mechanism, environment variable, compatibility API or customer restriction.

The only shared file touched is this report. Friction remains denied sockets,
the ancestor `/tmp/.git`, stale Overwing and the unported runtime fixtures.
Git has no local identity or core.worktree override. No Git write, installation
or stateful Sparkwing command ran. Bootstrap discovery invoked `sparkwing info
--for-agent` before the swarm brief's no-binary instruction was loaded. No
Sparkwing binary ran after that instruction was read. All checks finished and
`/tmp/sw-astra-W4/` was deleted. The lead retains pipelines, review and committing.

STATUS: CONTINUE
