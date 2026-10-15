# W4 continuation report

Continues `f30e4833c` and `W4-session10.md`.

Cross-pipeline `Ref.TryGet` now treats formatted JSON null as absent.
It previously returned `(nil, true)` for `"\n\t null \r\n"` while compact
`null` returned absence. The acceptance regression failed before the fix
and passes afterward. The comparison removes only JSON whitespace because
non-JSON whitespace must still fail decoding.

The acceptance controls cover compact and formatted null, the string
`"null"`, in-run null presence, required latest reads, child output reads,
and malformed non-JSON whitespace. C3 SDK notes clarify the existing null
semantics. No public signature, wire shape or external contract changed.

Deleted: 0 additional legacy lines. One production comparison was replaced.
No API was removed and no caller outside W4 needed migration. No new T1
inventory port was required. The existing `TestSecretIdentifiers` port passes.
Mechanisms added: none.

Linux acceptance passes:

- `protocol-hello-and-handles/local-go-linux`.
- `protocol-version-and-hard-cut/local-go-linux`.
- `protocol-go-sdk-fake-host-and-linkage/local-go-linux`, including the new
  output controls. `real-process-host-routes` skips because sockets are denied.
- `protocol-mask-acknowledgement/local-go-linux/sdk-service-contract`.
  Its enclosing cell remains skipped pending C2's Mask route contract.

Whole-repository build, W4/acceptance vet, focused W4 tests, Go process
conformance, schema snapshots, formatting and diff whitespace pass.
Nine test exclusions remain. Two reach C2's decoder stub, three filesystem
fallback tests encounter the harness's empty `/tmp/.git`, and four service
tests require sockets. No source skip or sandbox workaround was added.
No new race check was selected because the fix adds no shared state.

Reproduce after creating `/tmp/sw-astra-W4/cache` and `/tmp/sw-astra-W4/tmp`:

```sh
export GOWORK=off GOFLAGS=-buildvcs=false
export GOCACHE=/tmp/sw-astra-W4/cache GOTMPDIR=/tmp/sw-astra-W4/tmp TMPDIR=/tmp/sw-astra-W4/tmp
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 2 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 2 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./tests/acceptance -run 'TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v
```

Contract changes needed in other areas: C2 still must specify acknowledged
Mask registration's method, route, payload, response and authorization.
Its policy for masks matching accepted identifiers remains open.
No new cross-area contract change is requested.

Real integration remains open:

- W2's canonical decoder and acknowledged Mask route. Transport services
  are tested with fakes. `apierr.DecodeInner` remains an implementation stub.
- W6's resolved ceilings, `limits.Record` and process environment composition.
- W3's latest-output and child-await handlers with canonical child errors.
- W3/C1 persisted expansion, reconstruction hashes, claim identity, lifecycle,
  artifact/reference authorization and children through real placements.
- Socket-enabled checks, native Windows/macOS execution, remaining placement
  drivers and W5 TypeScript parity. The lead's approval SkipIf interpretation
  still needs confirmation in the contract notes.

Production file: `sparkwing/ref.go`. Shared files:
`tests/acceptance/protocol_output_test.go`,
`design/rewrite/contracts/c3-go-sdk-notes.md`, and this report.
No other area's production file changed. The lead retains independent review
and integrated gates. No Git writes or Sparkwing binary executions occurred.
The shared Git config has no injected worktree or user identity settings.

Friction: the sandbox prohibits sockets and exposes an empty `/tmp/.git`.
Overwing reports its installed binary behind source. Scratch files and Go
caches stayed in `/tmp/sw-astra-W4/`, which was deleted after verification.

STATUS: CONTINUE
