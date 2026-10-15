# W4 continuation report

Continues `d9b559088` and `W4-session7.md`.

SDK host-response decoding now refuses missing result fields. Secret responses
must include a string value and boolean masking classification. OIDC and cleanup
registration must return a nonempty token or ID. Direct outputs, latest outputs
and successful or memoized child results must contain an output member.
Explicit empty variables, `masked:false`, JSON-null outputs and additive fields
remain valid. A malformed latest-output response cannot become optional-reference
absence. The implementation uses the existing service interfaces and fake HTTP
transport. It adds no route, limit, setting, environment variable or helper API.

Regression evidence: all 17 incomplete-response cases failed before the fix.
The two successful-child cases missing output also failed before their fix.
Those cases and the valid controls now pass, including under the race detector.

Deleted: no additional legacy package or API removal (0 legacy lines). Four
production lines of permissive response handling were replaced. Earlier W4 hard
cuts remain in place. No production caller outside W4 needed migration.

Acceptance on Linux:

- `protocol-hello-and-handles/local-go-linux` passes.
- `protocol-version-and-hard-cut/local-go-linux` passes.
- `protocol-go-sdk-fake-host-and-linkage/local-go-linux` passes with its
  socket-dependent `real-process-host-routes` subtest skipped.
- `protocol-mask-acknowledgement/local-go-linux/sdk-service-contract` passes.
  The enclosing cell remains skipped because C2 has no acknowledged Mask route
  contract. No additional placement cell was activated.

Verification passed: whole-repository build, W4 and acceptance vet, focused W4
tests, Go process conformance and schema snapshots, race checks over sdkserve
and the named acceptance cells, and `git diff --check`.

Reproduce after creating `/tmp/sw-astra-W4/cache` and `/tmp/sw-astra-W4/tmp`:

```sh
export GOWORK=off GOFLAGS=-buildvcs=false
export GOCACHE=/tmp/sw-astra-W4/cache GOTMPDIR=/tmp/sw-astra-W4/tmp TMPDIR=/tmp/sw-astra-W4/tmp
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 2 ./internal/sdkserve ./tests/acceptance -run 'TestHostRoutes|TestServe|TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -v -count=1
```

The nine exclusions are unchanged from session 7: two nodeproto tests reach
W2's unimplemented canonical decoder, three Git-helper filesystem-fallback
tests encounter the harness's empty `/tmp/.git`, and four service tests require
prohibited sockets. Their prior failures were not rerun. No source skip or
production workaround was added. No new T1 inventory port was required.
The existing `TestSecretIdentifiers` port passes in the focused run.

Contract changes needed in other areas: C2 still must specify acknowledged Mask
registration's method, route, payload, response and authorization. No new
cross-area contract request arose. The local C3 Go notes now spell out required
host-response fields without changing wire keys or signatures. Published API
references and generated schemas are unchanged.

Real integration remains open:

- W2's canonical decoder for reply, recovery, body, eval and HTTP errors, plus
  acknowledged Mask registration and the accepted-identifier masking policy.
- W6's `limits.Record`, resolved ceilings and process environment composition.
- W3's latest-output and child-await handlers with canonical child errors.
  SDK transport behavior is tested with fakes, including malformed responses.
- W3/C1 persisted expansion and hash checks, claim identity, replay, lifecycle,
  artifact/reference authorization and children through real placements.
- Socket-enabled checks, native Windows/macOS execution, remaining placement
  drivers and W5 TypeScript parity. Approval SkipIf versus removed executable
  hooks still needs the lead's confirmation recorded in the contract notes.

Changed files: W4-owned `internal/sdkserve/routes.go` and `routes_test.go`.
Shared files: `design/rewrite/contracts/c3-go-sdk-notes.md` and this report.
No other area's production files changed. The lead retains independent review
and integrated pipeline gates. No Git writes occurred. The shared Git config
has no injected `core.worktree`, `user.name` or `user.email` entries.

Friction: the sandbox prohibits sockets and exposes an empty `/tmp/.git`.
Overwing reports its installed binary behind source. Bootstrap ran the installed
read-only `sparkwing info --for-agent` before the swarm brief's no-binary rule
was read. No subsequent Sparkwing binary, service, candidate or pipeline ran.
All scratch files and Go caches were confined to `/tmp/sw-astra-W4/`, which was
deleted after verification.

STATUS: CONTINUE
