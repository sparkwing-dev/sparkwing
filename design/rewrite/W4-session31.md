Continues [session 30](W4-session30.md) from committed HEAD `b4ea1e689`.
The worktree was clean on entry. No merge resolution remains unstaged.

The shared v2 corpus now pins the canonical hash of actual Go generator output.
The process driver checks repeated evaluation against that vector. A fresh
process reconstructs the same expansion under the generated job's delegated
binding, then executes the generated body. Every eval result also passes
`EvalResult.ValidateValue` for its closure kind, and body results pass typed
decoding. Existing schema snapshot tests pass without a wire-schema change.
These are SDK/process proofs. They do not prove persisted expansion acceptance
or host-side drift enforcement.

Amendment A1-51 gains one expansion/hash vector, a repeated-generation request,
and a fresh-process reconstruction session. W5 can consume `plan_vector`,
`binding_job` and per-step `job` from this shared corpus. W4's other amendment
implementations remain unchanged. No production API or contract signature changed.

Deleted legacy production code: **0 lines** in this continuation. The previous
W4 hard cuts remain intact. Owned production Go packages contain no unimplemented
marker or Old-suffixed API. No new inventory source test was ported, and no caller
outside W4 needed migration.

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W4/cache`, and `GOTMPDIR`/`TMPDIR=/tmp/sw-astra-W4/tmp`,
with `go -C /home/bombuntu/.xwing/envs/1082/sparkwing`.

Passed checks:

- `build -p 4 ./...` for the whole repository.
- `vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...`.
- `test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -count=1 -timeout 90s`, with the seven inherited exclusions below.
- `test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v -timeout 90s`.
- Focused conformance vet after the final driver edit, formatting and `git diff --check`.

The package exclusions were
`^(TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$`.
The first three encounter the sandbox's ancestor `/tmp/.git`. The remaining four
need sockets. They were not retried in this unchanged environment.

Linux hello/handles, version/hard-cut and available fake-host/linkage subtests
pass. Its real HTTP host-route subtest skips because sockets are denied.
The mask SDK-service subtest passes. The full mask target skips for W2/W3's real
route registration and sink installation. No new full placement cell is claimed.
Native Windows/Darwin, Docker, full backend/placement coverage, race reruns and
Sparkwing pipelines were not run for this test-only continuation.

Real integration remains with W2/W3 for authenticated mask/annotation/summary
routes, W6 for concurrency resolution, W3/W1 for persisted expansion and storage
authority, and W3/W8 for verified engine and granted runner delivery. Source
inspection still finds panic stubs in PolicyRouter, ResolveConcurrency and
Resolved.InstallSDK. W4's existing fakes exercise those declared SDK service
boundaries. W3/W8 own runtime fixture retirement, including the inherited nested
`.sparkwing` test port. W9 owns consumer docs and generated references. T1/lead
own native placement drivers and full QA evidence.

No new cross-area contract change or design question is raised. No runtime
mechanism, option, limit, environment variable or compatibility surface was added.
The remaining work requires other areas' implementations or native integration
evidence. Further W4 implementation should follow those integrations or a concrete
failing case rather than another speculative audit.

Changed owned files are `internal/nodeproto/conformance/conformance_test.go` and
`internal/nodeproto/conformance/testdata/v2-corpus.json`. Shared files touched are
`design/rewrite/contracts/c3-go-sdk-notes.md` and this report. No other area's
implementation file changed.

Friction remains denied sockets, ancestor `/tmp/.git`, stale Overwing and the
unported runtime fixtures. Git has no local identity or core.worktree override.
No Sparkwing binary or Git write ran. All checks finished and scratch under
`/tmp/sw-astra-W4/` was deleted. The lead retains review, pipelines and committing.

STATUS: CONTINUE
