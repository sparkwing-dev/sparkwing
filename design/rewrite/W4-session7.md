# W4 continuation report

Continues `ede0691c3` and `W4-session6.md`.

Fixed SDK frame sizing to include the terminating newline. The prior writer could publish a log that its reader refused because the encoded payload filled the entire line budget. The acceptance regression failed before the one-line fix and passes afterward. It exercises `sdkserve.Serve` with fake SDK operations, accepts a complete line at the limit, and refuses an oversized line before writing it. The protocol notes now state that framing counts toward the existing limit. No new limit, interface, environment variable or other mechanism was added.

Deleted 91 lines of unused SDK implementation and associated comments: 87 production lines and a four-line test helper. These remove admission context state, the old risk error, unused admission/priority/claim-wait/dry-run/enum getters, the duplicate job environment field, and a detached approval comment. Updated author documentation for memo versus directory cache, positive capacity, ClaimWait validation, typed trigger Inputs and dry-run execution. No production caller outside W4 needed migration.

Acceptance evidence on Linux, including the race detector:

- `protocol-hello-and-handles/local-go-linux` passes.
- `protocol-version-and-hard-cut/local-go-linux` passes.
- `protocol-go-sdk-fake-host-and-linkage/local-go-linux` passes, including the new `sdk-frame-boundaries/{at-limit,over-limit}` subtests. Its socket-dependent host-route subtest skips.
- `protocol-mask-acknowledgement/local-go-linux/sdk-service-contract` passes. Its enclosing cell remains skipped pending C2's acknowledged Mask route contract.

No additional full placement cell was unskipped. No new T1 inventory port was required. The existing `TestSecretIdentifiers` port passes.

Verification passed:

- Whole-repository `go build -p 4 ./...`.
- Vet over all W4 packages and both acceptance packages.
- Focused W4 tests, including Go process conformance and schema snapshots, with the nine exclusions below.
- Race tests over `internal/sdkserve`, `sparkwing` and `sparkwing/sparkwingtest`. Acceptance ran under the race detector after the framing fix.
- Focused unused-code lint reports zero issues. Commentcheck against HEAD and `git diff --check` pass.

The unrestricted W4 package test run was attempted. Two nodeproto tests panic in W2's unimplemented `apierr.DecodeInner`. Three retained Git-helper filesystem-fallback tests encounter the harness's empty `/tmp/.git` mount. Four service tests require prohibited sockets. These are command-line exclusions only. No source skip or production workaround was added.

Reproduce with `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-astra-W4/cache GOTMPDIR=/tmp/sw-astra-W4/tmp TMPDIR=/tmp/sw-astra-W4/tmp`, after creating those directories:

```sh
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$'
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 2 ./tests/acceptance -run 'TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -v
```

Contract changes needed in other areas: C2 must specify the acknowledged Mask registration route, payload, response and authorization. Its policy for masks matching accepted identifiers and the lead's approval SkipIf interpretation remain open. The W4 note change only clarifies existing newline accounting.

Real integration remains open:

- W2's canonical decoder for reply/recovery/body/eval/HTTP errors and its Mask registration contract and implementation.
- W6's `limits.Record`, resolved limits and process environment composition with real hosts.
- W3's latest-output and child-await handlers using `job` and canonical child errors. W4's HTTP transport tests use fakes against those contracts.
- W3/C1 expansion acceptance using existing jobs and plan concurrency, persisted hashes, claim identity, replay, lifecycle decisions, artifact authorization, references and children through real placements.
- Socket-dependent integration, native Windows execution and the remaining placement drivers. TypeScript remains W5's area.

Shared files touched: `tests/acceptance/protocol_test.go`, new `tests/acceptance/protocol_frame_test.go`, `design/rewrite/contracts/c3-protocol-notes.md`, and this report. All other edits are W4-owned SDK or protocol-serving files.

Friction: the sandbox prohibits sockets and mounts an empty `/tmp/.git`. Overwing reports its installed binary behind source. No Sparkwing binary, service, candidate or pipeline ran. No Git writes occurred. The repository config has no injected `core.worktree` or `user.name`. The lead retains integrated gates and independent review. `/tmp/sw-astra-W4/` was deleted after verification.

STATUS: CONTINUE
