# W4 continuation report

Continues `8b8a3f6b1`. The repository builds. This session fixes three remaining SDK defects and adds host-route tests against a fake transport. The cross-area and native-platform gaps below remain open.

## Implemented

- New node-wide attempts clear step outputs and completion state before their first body. `StepGet` sees the current attempt, including after a failed or skipped prerequisite. Parallel steps within one attempt share their results. Stale ordinals and attempts overlapping active bodies fail before resetting state.
- Protocol planning accepts an SDK entrypoint whose name differs from the invoked project pipeline. The fake host preserves a nonempty `Options.Run.Pipeline` alias. Actual process and fake-host tests verify that author code receives the alias.
- `RunAndAwait` and required cross-pipeline references fail when the service returns no output bytes. Optional references still report absence. Explicit JSON null retains its previous semantics. Corrupt output and service errors remain failures.
- Host-service tests use a per-instance fake HTTP transport. They verify bearer binding, escaped secret/job/pipeline paths, latest-output queries, child submission and terminal success/memoized output, OIDC, cleanup routes, invalid-origin refusal and cancellation propagation without sockets.
- Updated C3 notes and source API comments. The changed request corpus and acceptance probes check the documented behavior.

## Deletions and ports

Deleted the unused `copyLabels` helper and `keyPipelineConfig` key, 10 lines total. Removed the old empty-output success behavior and the entrypoint-name equality check. The tracked diff removes 37 lines overall and deletes no files.

Migrated `TestRunAndAwait_EmptyDataZeroValue` and `TestRefToLastRun_Get_EmptyDataProducesZeroValue` into missing-output failure tests. Updated the request-shape corpus to accept project aliases and still reject a missing invoked name. The earlier T1 `TestSecretIdentifiers` port remains unchanged. No new T1 source inventory item was ported.

## Acceptance and checks

These local Linux Go cells pass:

- `protocol-hello-and-handles/local-go-linux`.
- `protocol-version-and-hard-cut/local-go-linux`.
- `protocol-go-sdk-fake-host-and-linkage/local-go-linux`, including the new `entrypoint-alias`, `output-failures` and `real-process-application-retry` subtests. Its HTTP process-route subtest still skips because sockets are prohibited.
- The `sdk-service-contract` subtest of `protocol-mask-acknowledgement/local-go-linux` passes. The enclosing cell stays skipped pending the C2 registration contract.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Vet over `internal/nodeproto/...`, `internal/protofd`, `internal/sdkserve`, `sparkwing/...`, `internal/acceptance` and `tests/acceptance`.
- Tests for nodeproto and conformance, protofd, sdkserve, the root SDK, inputs, cleanup and sparkwingtest. The nodeproto selection excludes exactly `TestParseFrameV2Shapes` and `TestReplyDecodeV2`, which consume W2's stubbed decoder. No source skip was added.
- Focused race checks for attempt state, references, child calls, fake hosts, host-route transport and server lifecycle.
- Windows/amd64 compilation of the sdkserve and acceptance test executables. Native Windows execution was unavailable.
- Comment and sleep checks against HEAD, plus `git diff --check`.

The full `go test -p 4 ./sparkwing/...` run passes the root SDK, cleanup, Docker, inputs and sparkwingtest. It fails three Git filesystem-fallback tests because the sandbox places an empty `.git` mount above the temporary fixtures. `listGitFiles` discovers that mount, then Git refuses it. It also fails four service-port tests because the sandbox refuses sockets. These failures remain visible and their files were not changed. A direct nodeproto error-decoding run still panics in `apierr.DecodeInner`, confirming the C2 dependency.

All Go checks used `GOWORK=off GOFLAGS=-buildvcs=false`, `GOCACHE=/tmp/sw-astra-W4/cache`, `GOTMPDIR=/tmp/sw-astra-W4/tmp`, and `TMPDIR=/tmp/sw-astra-W4/tmp`. Reproduce the focused checks with those variables and `go -C /home/bombuntu/.xwing/envs/1082/sparkwing`:

```sh
go build -p 4 ./...
go test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing ./sparkwing/inputs ./sparkwing/cleanup ./sparkwing/sparkwingtest -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2)$'
go test -p 4 ./tests/acceptance -run 'TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -v
go test -race -p 4 ./sparkwing ./sparkwing/sparkwingtest ./internal/sdkserve -run 'TestProtocolAttempt|TestRunAndAwait|TestRefTryGet|TestFakeHost|TestHostRoutes|TestServe'
```

## Contract changes needed and integration left open

- C2 must define acknowledged Mask registration, including route, payload, response and authorization. Its policy for dynamic masks matching accepted identifiers remains open. W4 does not invent that route.
- W2 must implement `apierr.DecodeInner` for failed replies, failed AfterRun input, recovery errors and HTTP errors.
- W6 must implement `limits.Record` for oversized annotations and summaries. Environment composition and resolved limits still need real-host integration.
- W3 must migrate retained latest-output and child-await handlers from `node` to `job`, and emit the canonical child error. The new fake-transport tests pin the SDK requests without claiming real-handler compatibility.
- W3/C1 expansion validation must receive accepted jobs and plan-level `ExistingConcurrency`. Real placement drivers still own persistence, claim identity, replay/hash checks, lifecycle decisions, artifact authorization, references and child execution.
- Run the socket-dependent tests outside this sandbox and the portable process probes on native Windows. TypeScript remains W5's area.

No new change to another area's contract is requested. W4 notes clarify attempt state and the already retained entrypoint alias semantics. The existing approval SkipIf distinction and Mask policy questions remain with the lead.

## Shared files touched

- `design/rewrite/contracts/c3-go-sdk-notes.md`
- `design/rewrite/contracts/c3-protocol-notes.md`
- `tests/acceptance/protocol_test.go`
- `tests/acceptance/protocol_alias_test.go`
- `tests/acceptance/protocol_output_test.go`
- `tests/acceptance/protocol_retry_test.go`
- `tests/acceptance/testdata/protocolgo/fixture.go`
- `tests/acceptance/testdata/protocolgo/retry.go`
- This report.

No production caller outside W4 changed this session.

## Review, assumptions and friction

An independent Astra audit identified the retry and alias defects. Both were reproduced before correction. A fresh Astra review of an immutable source snapshot reported no actionable findings and accepted both mechanisms below as proportionate.

- Attempt state adds an ordinal and active-body count under Work's existing mutex. Replacing only the last output would leave skipped prerequisites stale and allow a reset during an active body. Removing this state would restore those defects.
- A private `http.RoundTripper` field permits socket-free route tests without replacing global HTTP state. Production retains the default transport. Removing the field would leave route testing dependent on listeners prohibited by this sandbox.

No public API, environment variable, limit, compatibility layer or service was added. Overwing reports that its installed binary trails its source. The child-await fixture was corrected from 30 seconds to C4's declared 60 seconds before verification passed. The full SDK test failures above are environment limits, not passes.

No repository Git writes, Sparkwing binaries, pipelines or live services ran. Local Git config has no injected `core.worktree` or `user.name` override. No process executable remained beneath the session's temporary directory. The lead owns commits and integrated gates. `/tmp/sw-astra-W4/` is removed at handoff.

STATUS: CONTINUE
