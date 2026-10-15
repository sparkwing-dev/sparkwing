# W4 continuation report

Continues `7292c2036` and `W4-session9.md`.

`sparkwingtest.Host.Plan` now refuses execution services during declarations.
Previously it supplied fixture secrets and outputs, invoked latest-output,
child-run and OIDC callbacks, and acknowledged masks. A real planning process
has no execution binding. The fake host now checks the declaration context
before reading fixture answers or invoking callbacks. Declaration logs remain
available without job attribution.

The new `declaration-services` acceptance subtest failed for all seven access
paths before the fix. It also observed three execution callbacks and two new
masks. It passes after the fix, including a successful declaration-log control.
The C3 Go SDK notes record the shared behavior. Public signatures, wire shapes
and error codes are unchanged.

Deleted: 0 additional legacy lines. Six cancellation-only checks now use the
same execution-context check. No API was removed, and no production caller
outside W4 needed migration. No new T1 inventory port was required. The existing
`TestSecretIdentifiers` port passes.

Acceptance on Linux:

- `protocol-hello-and-handles/local-go-linux` passes.
- `protocol-version-and-hard-cut/local-go-linux` passes.
- `protocol-go-sdk-fake-host-and-linkage/local-go-linux` passes, including the
  new `declaration-services` subtest. Its `real-process-host-routes` subtest
  skips because the sandbox prohibits sockets.
- `protocol-mask-acknowledgement/local-go-linux/sdk-service-contract` passes.
  The enclosing cell remains skipped pending C2's Mask route contract.
  No additional placement cell was activated.

Verification: whole-repository build, W4 and acceptance vet, focused W4 tests,
Go process conformance, schema snapshots and diff whitespace passed. Race checks
passed over the core W4 packages and selected Linux acceptance cells.

Reproduce after creating `/tmp/sw-astra-W4/cache` and `/tmp/sw-astra-W4/tmp`:

```sh
export GOWORK=off GOFLAGS=-buildvcs=false
export GOCACHE=/tmp/sw-astra-W4/cache GOTMPDIR=/tmp/sw-astra-W4/tmp TMPDIR=/tmp/sw-astra-W4/tmp
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 2 ./internal/nodeproto ./internal/sdkserve ./sparkwing ./sparkwing/sparkwingtest -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 2 ./tests/acceptance -run 'TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1
```

The nine focused-test exclusions remain: two tests reach C2's decoder stub,
three filesystem-fallback tests encounter the harness's empty `/tmp/.git`,
and four service tests require sockets. A focused run of the three Git tests
with `GIT_CEILING_DIRECTORIES=/tmp/sw-astra-W4/tmp` still failed because
`listGitFiles` walks ancestor `.git` paths itself. No source workaround or
test skip was added for the sandbox.

Contract changes needed in other areas: C2 must specify acknowledged Mask
registration's method, route, payload, response and authorization. Its policy
for masks matching accepted identifiers remains open. No new cross-area
contract change is requested.

Real integration remains open:

- W2's canonical decoder for reply, recovery, body, eval and HTTP errors.
  `routeServices.Mask` still returns `unavailable` because C2 has no
  registration contract. SDK acknowledgement is covered with fake services.
- W6's `limits.Record`, resolved ceilings and process environment composition.
- W3's latest-output and child-await handlers with canonical child errors.
  W4 transport behavior is covered with fake routes.
- W3/C1 persisted expansion and hash checks, claim identity, replay, lifecycle,
  artifact/reference authorization and children through real placements.
- Socket-enabled checks, native Windows/macOS execution, remaining placement
  drivers and W5 TypeScript parity. The lead's approval SkipIf interpretation
  still needs confirmation in the contract notes.

The only production file changed is `sparkwing/sparkwingtest/host.go`.
Shared files: `tests/acceptance/protocol_declaration_test.go`,
`tests/acceptance/protocol_test.go`,
`design/rewrite/contracts/c3-go-sdk-notes.md`, and this report.
No other area's production file changed. The lead retains independent review
and integrated pipeline gates. No Git writes occurred. The shared repository
config has no injected `core.worktree`, `user.name` or `user.email` entries.

Mechanisms added: none. The existing declaration marker supplies the check.
No new option, environment variable, interface, service or compatibility
surface was added.

Friction: sockets are prohibited and the harness exposes an empty `/tmp/.git`.
Overwing reports its installed binary behind source. The read-only bootstrap
`sparkwing info --for-agent` ran before the swarm brief's no-binary rule was
read. No subsequent Sparkwing binary, candidate, service or pipeline ran.
All scratch files and Go caches stayed in `/tmp/sw-astra-W4/`, which was
deleted after verification. No live executables remained under that directory.

STATUS: CONTINUE
