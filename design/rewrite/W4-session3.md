# W4 continuation report

Continues `daf9bebe4`. The whole repository builds. W4's remaining integration depends on the C2/C4 implementations, real host drivers and native platform execution listed below.

## Implemented

- The server checks binding and duplicate active steps before preparing body environment. Describe runs as tracked work, so author callbacks cannot block reading a cancellation. Repeated planning cannot wait on an active step. Dispatch captures the run context before starting workers. Completed steps leave the active set before their reply becomes visible, allowing an immediate next attempt.
- Expansion validation preserves concurrency capacity, scope and on-limit policy across existing jobs and plan memberships. Different membership costs remain valid. Added the W4-local `ValidationContext.ExistingConcurrency` field and supplied it from SDK generator evaluation.
- Restored the contract's retained `StepError` type, its error methods and `ConcurrencyGroup.String`. The removed SDK exports remain absent. This does not restore the deleted work scheduler or alter canonical wire error messages.
- Fake-host step records carry the invocation's job identity, including concurrent steps. Eval records remain run logs. Cancelled OIDC, reference and child calls refuse before invoking fixture callbacks.
- Cleanup and session-ledger code use the existing C4 runtime name `SPARKWING_JOB_ID`. Removed the three production reads of `SPARKWING_NODE_ID`. Migrated the cleanup, Docker and Windows tests and the host's attempt fixture.
- Extracted the existing Go process fixture's lifecycle into one reusable driver. New acceptance probes exercise native pipes, runner identity, stdout/stderr separation, live cancellation, unknown fields, version mismatch and removed wire/API surfaces.

## Deletions and ports

This session removes 147 tracked lines and deletes no files. Most removed lines were moved out of the old process fixture or replaced by the corrected server dispatch. The obsolete production surface removed here is the three old runtime-variable reads and stale SDK comments about Runtime, WithRunner and gitcache. Earlier sessions' file deletions are not counted again.

The retained cleanup/session-ledger tests now use the final job variable. A negative test proves the retired node variable cannot register a cleanup. The earlier `TestSecretIdentifiers` inventory port remains in place. No other area's legacy tests were deleted. Added regressions cover every fix above and compile probes for the retained and removed SDK APIs.

## Acceptance and verification

The following local Go cells are enabled and pass on Linux:

- `protocol-hello-and-handles/local-go-linux`: unbound planning, bound execution and cancellation of a running body. The fixture supplies granted identity and credentials over the actual private process pipes. This proves the SDK boundary, not real claim-award integration.
- `protocol-version-and-hard-cut/local-go-linux`: mismatched hello, additive field, feature advertisement, removed run_node, removed document protocol field, supported public API compilation and nine independent removed-API compile failures.
- `protocol-go-sdk-fake-host-and-linkage/local-go-linux`: linkage, fake host, process plan/output and body failures pass. Its HTTP-route subtest skips because the sandbox refuses loopback sockets.
- The `sdk-service-contract` subtest of `protocol-mask-acknowledgement/local-go-linux` passes. The containing cell remains skipped until C2 defines acknowledged registration.

Checks passed:

- Root `go build -p 4 ./...`, including after the final server change.
- Vet over `internal/nodeproto/...`, `internal/protofd`, `internal/sdkserve`, `sparkwing/...`, `internal/acceptance` and `tests/acceptance`.
- Tests over `internal/nodeproto/...`, `internal/protofd`, `internal/sdkserve`, `sparkwing`, `sparkwing/inputs`, `sparkwing/cleanup` and `sparkwing/sparkwingtest`, excluding exactly `TestParseFrameV2Shapes` and `TestReplyDecodeV2`. Those two still require C2's stubbed `apierr.DecodeInner`; neither was removed or skipped in source.
- Targeted race tests for server lifecycle, fake hosts, expansion concurrency and generated-job replay. The final blocking-Describe change passed the entire server suite under the race detector.
- The seven Go v2 conformance sessions and schema snapshots. Conformance and the selected acceptance cells were rerun after the final server change.
- Windows/amd64 compilation of the SDK, protofd and acceptance test executables. Native Windows execution was not available.
- `git diff --check`. Local Git config has no injected `core.worktree` or `user.name` override.

Every Go command used `GOWORK=off GOFLAGS=-buildvcs=false`, `-p 4`, and `GOCACHE`, `GOTMPDIR` and `TMPDIR` beneath `/tmp/sw-astra-W4/`. Reproduce the tests with those variables set and `go -C /home/bombuntu/.xwing/envs/1082/sparkwing`:

```sh
go test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing ./sparkwing/inputs ./sparkwing/cleanup ./sparkwing/sparkwingtest -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2)$'
go test -p 4 ./tests/acceptance -run 'TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -v
go test -race -p 4 ./internal/sdkserve ./sparkwing/sparkwingtest ./internal/nodeproto ./sparkwing -run 'TestServe|TestFakeHost|TestExpansionPreservesExistingConcurrency|TestBoundGeneratedJobReplaysThroughProtocol'
```

## Contract changes and real integration left open

- C2 must define the acknowledged Mask route, request, response and authorization policy. Dynamic masks colliding with already accepted identifiers still need that registration policy. Real transport continues to return canonical unavailable rather than guess a route.
- W2 must implement `apierr.DecodeInner`. Full error-consumer tests and failed AfterRun decoding remain dependent on it. The independent process fixture judges emitted errors without substituting an implementation in production.
- W6 must implement `limits.Record` for oversized annotations and summaries. Host environment composition and resolved limits still need integration.
- W3/C1 expansion callers must supply both accepted jobs and `ExistingConcurrency` when they adopt the shared validator. This field is a W4 contract addition, not a change to another area's contract.
- W3's retained HTTP handlers still read `node` query parameters for latest-output and child-await, while the SDK uses `job`; child failure payloads also need the canonical inner error. The host migration must settle these routes before their real-integration cells can pass. No handler was rewritten here.
- Real placement drivers still own strict-plan persistence, claim identity, sealed replay, lifecycle layers, artifacts, references and children. Their skipped cells are not passes. Run the HTTP tests outside the socket sandbox and the portable pipe/acceptance fixtures on native Windows. TypeScript remains W5's area.

## Shared files changed

- `design/rewrite/contracts/c3-go-sdk-notes.md`
- `design/rewrite/contracts/c3-protocol-notes.md`
- `internal/acceptance/protocol_go.go`
- `internal/acceptance/protocol_process.go`
- `internal/nodehost/testdata/attemptfixture/main.go` - only the runtime job-variable spelling changed.
- `tests/acceptance/protocol_hard_cut_test.go`
- `tests/acceptance/protocol_process_test.go`
- `tests/acceptance/protocol_test.go`
- `tests/acceptance/testdata/protocolgo/fixture.go`
- `tests/acceptance/testdata/protocolgo/probe.go`
- This report, `design/rewrite/W4-session3.md`.

## Review, assumptions and friction

A read-only Astra audit found the two missing retained exports, expansion concurrency conflict and blocking-Describe case. Each is fixed. The final focused review reported no actionable defect and found the additions proportionate.

The added mechanisms are the concurrency-context slice, an immutable per-invocation service binding in the fake host, and the extracted acceptance process driver. ExistingJobs alone cannot represent plan concurrency. A shared mutable fake-host job would misattribute concurrent logs. Extracting the driver avoids duplicating pipe startup and child reaping. No environment variable, compatibility alias or runtime service was added.

Sockets remain prohibited and native Windows is unavailable. Overwing reports that its installed binary trails its source. A completion-regression fixture initially used synctest around a mutex wait, which cannot become durably blocked; those test processes were terminated, and the fixture now uses explicit channel barriers. The corrected test and race suite pass.

No Git writes, Sparkwing binaries, pipelines or live services ran. `/tmp/sw-astra-W4/` was deleted at handoff. The lead owns committing and integrated gates.

STATUS: CONTINUE
