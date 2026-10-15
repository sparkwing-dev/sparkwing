# W4 continuation report

Continues `d38bf1a3a` and `W4-session5.md`.

`sparkwingtest.New` now copies Git and pull-request metadata. Changing an options value for a second fixture no longer changes the first fixture's plans. The public acceptance regression failed before the fix and passes afterward. The SDK contract note and constructor documentation describe the snapshot boundary. Service callbacks remain test-owned dependencies.

No additional legacy implementation was deleted (0 lines). One constructor comment was replaced. The earlier hard cuts remain intact. No Old-suffixed declarations or unimplemented panic bodies were found in W4 production Go files. No new T1 inventory test was ported. The existing `TestSecretIdentifiers` port remains covered.

Passed checks:

- `protocol-hello-and-handles/local-go-linux`.
- `protocol-version-and-hard-cut/local-go-linux`.
- `protocol-go-sdk-fake-host-and-linkage/local-go-linux`, including the new `fake-host-run-snapshot` case. Its HTTP route subtest skips because sockets are prohibited.
- `protocol-mask-acknowledgement/local-go-linux/sdk-service-contract`. The enclosing cell still skips because C2 has no acknowledged Mask route contract.
- Focused W4 tests, including Go conformance and schema snapshots. The selection excludes exactly `TestParseFrameV2Shapes` and `TestReplyDecodeV2`, which require W2's stubbed `apierr.DecodeInner`. No source skip was added.
- Vet over W4 and acceptance packages. Comment and test-wait checks against HEAD pass.

Whole-repository `go build -p 2 ./...` passes. `git diff --check` passes.

Reproduction uses `GOWORK=off GOFLAGS=-buildvcs=false`, `GOCACHE=/tmp/sw-astra-W4/cache`, `GOTMPDIR=/tmp/sw-astra-W4/tmp` and `TMPDIR=/tmp/sw-astra-W4/tmp`. Recreate those directories before running:

```sh
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 2 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 1 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 2 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing ./sparkwing/inputs ./sparkwing/cleanup ./sparkwing/sparkwingtest -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2)$'
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 1 ./tests/acceptance -run 'TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -v
```

Contract changes needed in other areas remain unchanged: C2 must specify the acknowledged Mask registration route, payload, response and authorization. Its policy for masks matching accepted identifiers and the lead's approval SkipIf interpretation remain open. This session adds no new contract shape, API, environment variable, limit or mechanism.

Real integration remains open:

- W2 must implement `apierr.DecodeInner` for failed replies, recovery errors, failed AfterRun input and HTTP error decoding.
- W6 must implement `limits.Record` for oversized annotation and summary tests, and integrate process environment composition and resolved limits with real hosts.
- W3 must align latest-output and child-await handlers with `job` and canonical child errors. Existing W4 tests pin requests through fake HTTP transport.
- W3/C1 must supply accepted jobs and `ExistingConcurrency` during expansion validation and exercise persistence, claim identity, replay hashes, lifecycle decisions, artifact authorization, references and children through real placements.
- Socket-dependent integration and native Windows execution need a suitable host. TypeScript remains W5's area.

Shared files touched are `tests/acceptance/protocol_test.go`, the new `tests/acceptance/protocol_snapshot_test.go`, `design/rewrite/contracts/c3-go-sdk-notes.md`, and this report. No production caller outside W4 changed.

Friction: Overwing reports its installed binary behind source. Bootstrap discovery ran installed `sparkwing info --for-agent` before the swarm brief was read. No further Sparkwing command, candidate, pipeline or service ran. No Git writes occurred, and the repository config has no injected `core.worktree` or `user.name`. The lead owns integrated gates and independent review. `/tmp/sw-astra-W4/` was deleted after verification.

STATUS: CONTINUE
